use super::{MAX_PAYLOAD, Result, Values};
use aws_lc_rs::{
    aead::{self, Aad, LessSafeKey, Nonce, UnboundKey},
    encoding::{AsDer, PublicKeyX509Der},
    rand::{SecureRandom, SystemRandom},
    rsa::{
        KeySize, OAEP_SHA256_MGF1SHA256, OaepPrivateDecryptingKey, OaepPublicEncryptingKey,
        PrivateDecryptingKey, PublicEncryptingKey,
    },
};
use base64::{Engine, engine::general_purpose::STANDARD};
use der::Decode;
use serde::{Deserialize, Serialize};
use zeroize::Zeroizing;
const PROTOCOL: &str = "ghenv-v1";

pub struct PrivateKey(PrivateDecryptingKey);
impl PrivateKey {
    pub fn generate() -> Result<Self> {
        Ok(Self(
            PrivateDecryptingKey::generate(KeySize::Rsa3072)
                .map_err(|_| "cannot generate RSA key")?,
        ))
    }
    pub fn from_pkcs8(bytes: &[u8]) -> Result<Self> {
        Ok(Self(
            PrivateDecryptingKey::from_pkcs8(bytes).map_err(|_| "invalid private key")?,
        ))
    }
    pub fn public_key(&self) -> Result<String> {
        let public = self.0.public_key();
        let der =
            AsDer::<PublicKeyX509Der>::as_der(&public).map_err(|_| "cannot encode public key")?;
        Ok(STANDARD.encode(der.as_ref()))
    }
}
#[derive(Serialize, Deserialize)]
struct Envelope {
    version: String,
    key: String,
    nonce: String,
    ciphertext: String,
}
fn decode(value: &str) -> std::result::Result<Vec<u8>, base64::DecodeError> {
    STANDARD.decode(value.replace(['\r', '\n'], ""))
}
pub fn seal(public: &str, values: &Values, request_id: &str) -> Result<Vec<u8>> {
    if public.len() > 8192 {
        return Err("invalid public key encoding".into());
    }
    let der = decode(public).map_err(|_| "invalid public key encoding")?;
    let public = PublicEncryptingKey::from_der(&der).map_err(|_| "invalid public key")?;
    let spki = spki::SubjectPublicKeyInfoRef::from_der(&der).map_err(|_| "invalid public key")?;
    let rsa = pkcs1::RsaPublicKey::from_der(
        spki.subject_public_key
            .as_bytes()
            .ok_or("invalid public key")?,
    )
    .map_err(|_| "invalid public key")?;
    if !(3072..=4096).contains(&public.key_size_bits())
        || rsa.public_exponent.as_bytes() != [1, 0, 1]
    {
        return Err("expected a 3072-4096 bit RSA public key".into());
    }
    let mut plaintext = Zeroizing::new(serde_json::to_vec(values)?);
    if plaintext.len() > MAX_PAYLOAD {
        return Err("environment is too large".into());
    }
    let rng = SystemRandom::new();
    let mut key = Zeroizing::new([0_u8; 32]);
    let mut nonce = [0_u8; 12];
    rng.fill(key.as_mut())
        .map_err(|_| "cannot generate payload key")?;
    rng.fill(&mut nonce)
        .map_err(|_| "cannot generate payload nonce")?;
    let cipher = LessSafeKey::new(
        UnboundKey::new(&aead::AES_256_GCM, key.as_ref()).map_err(|_| "invalid payload key")?,
    );
    cipher
        .seal_in_place_append_tag(
            Nonce::assume_unique_for_key(nonce),
            Aad::from(request_id.as_bytes()),
            &mut *plaintext,
        )
        .map_err(|_| "cannot encrypt payload")?;
    let public = OaepPublicEncryptingKey::new(public).map_err(|_| "invalid public key")?;
    let mut wrapped = vec![0; public.ciphertext_size()];
    let wrapped = public
        .encrypt(
            &OAEP_SHA256_MGF1SHA256,
            key.as_ref(),
            &mut wrapped,
            Some(PROTOCOL.as_bytes()),
        )
        .map_err(|_| "cannot encrypt payload key")?;
    Ok(serde_json::to_vec(&Envelope {
        version: PROTOCOL.into(),
        key: STANDARD.encode(wrapped),
        nonce: STANDARD.encode(nonce),
        ciphertext: STANDARD.encode(plaintext.as_slice()),
    })?)
}
pub fn unseal(private: &PrivateKey, data: &[u8], request_id: &str) -> Result<Values> {
    if data.len() > MAX_PAYLOAD * 2 {
        return Err("encrypted payload is too large".into());
    }
    let envelope: Envelope =
        serde_json::from_slice(data).map_err(|_| "invalid encrypted payload")?;
    if envelope.version != PROTOCOL {
        return Err("unsupported encryption protocol".into());
    }
    let wrapped = decode(&envelope.key).map_err(|_| "invalid encrypted payload")?;
    let private = OaepPrivateDecryptingKey::new(private.0.clone())
        .map_err(|_| "cannot decrypt payload key")?;
    let mut buffer = Zeroizing::new(vec![0; private.min_output_size()]);
    let key = private
        .decrypt(
            &OAEP_SHA256_MGF1SHA256,
            &wrapped,
            &mut buffer,
            Some(PROTOCOL.as_bytes()),
        )
        .map_err(|_| "cannot decrypt payload key")?;
    if key.len() != 32 {
        return Err("invalid payload key length".into());
    }
    let cipher = LessSafeKey::new(
        UnboundKey::new(&aead::AES_256_GCM, key).map_err(|_| "invalid payload key")?,
    );
    let nonce: [u8; 12] = decode(&envelope.nonce)
        .map_err(|_| "invalid encrypted payload")?
        .try_into()
        .map_err(|_| "invalid payload nonce")?;
    let mut ciphertext =
        Zeroizing::new(decode(&envelope.ciphertext).map_err(|_| "invalid encrypted payload")?);
    let plaintext = cipher
        .open_in_place(
            Nonce::assume_unique_for_key(nonce),
            Aad::from(request_id.as_bytes()),
            &mut ciphertext,
        )
        .map_err(|_| "payload authentication failed")?;
    serde_json::from_slice(plaintext).map_err(|_| "invalid environment payload".into())
}
