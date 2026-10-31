fn main() {
    for key in ["VERSION", "COMMIT"] {
        println!("cargo:rerun-if-env-changed={key}");
        let fallback = if key == "VERSION" { "dev" } else { "unknown" };
        let value = std::env::var(key).unwrap_or_else(|_| fallback.into());
        assert!(!value.contains(['\n', '\r']), "invalid build metadata");
        println!("cargo:rustc-env=GHENV_{key}={value}");
    }
}
