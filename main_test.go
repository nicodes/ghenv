package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func keyPair(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key, base64.StdEncoding.EncodeToString(der)
}

func TestEncryption(t *testing.T) {
	key, public := keyPair(t)
	want := map[string]string{"SECRET": "quoted ' \" $()\nsecond line=ok 😀", "EMPTY": ""}
	payload, err := seal(public, want, "request")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte(want["SECRET"])) {
		t.Fatal("plaintext leaked into envelope")
	}
	got, err := unseal(key, payload, "request")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip failed: %v", err)
	}
	if _, err = unseal(key, payload, "different-request"); err == nil {
		t.Fatal("accepted wrong request ID")
	}
	otherKey, _ := keyPair(t)
	if _, err = unseal(otherKey, payload, "request"); err == nil {
		t.Fatal("accepted wrong private key")
	}
	var e envelope
	if err = json.Unmarshal(payload, &e); err != nil {
		t.Fatal(err)
	}
	e.Ciphertext[0] ^= 1
	bad, _ := json.Marshal(e)
	if _, err = unseal(key, bad, "request"); err == nil {
		t.Fatal("accepted tampered ciphertext")
	}
	e.Nonce = []byte("bad")
	bad, _ = json.Marshal(e)
	if _, err = unseal(key, bad, "request"); err == nil {
		t.Fatal("accepted bad nonce")
	}
	if _, err = unseal(key, []byte(`{"version":"unknown"}`), "request"); err == nil {
		t.Fatal("accepted unknown protocol")
	}
	if _, err = seal("bad-key", want, "request"); err == nil {
		t.Fatal("accepted invalid public key")
	}
}

func TestSelection(t *testing.T) {
	secrets := map[string]string{"TOKEN": "synthetic secret", "EMPTY": "", "UNREQUESTED": "must stay remote"}
	variables := map[string]string{"URL": "https://example.com"}
	got, err := selectValues(secrets, variables, "TOKEN,EMPTY", "URL")
	if err != nil || len(got) != 3 || got["EMPTY"] != "" {
		t.Fatalf("selection failed: %v", err)
	}
	if _, ok := got["UNREQUESTED"]; ok {
		t.Fatal("exported unrequested secret")
	}
	for _, tc := range []struct{ secret, variable string }{
		{"MISSING", ""}, {"TOKEN", "TOKEN"}, {"GITHUB_TOKEN", ""},
		{"github_token", ""}, {"TOKEN,", ""}, {"BAD-NAME", ""}, {"", ""},
	} {
		if _, err := selectValues(secrets, variables, tc.secret, tc.variable); err == nil {
			t.Fatalf("accepted invalid selection %+v", tc)
		}
	}
	secrets["TOKEN"] = "bad\x00value"
	if _, err := selectValues(secrets, nil, "TOKEN", ""); err == nil {
		t.Fatal("accepted NUL byte")
	}
}

func makeZip(t *testing.T, name string, payload []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	f, err := w.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestFetchEnvironment(t *testing.T) {
	for _, mode := range []string{"success", "failed-run", "missing-artifact", "tampered", "extra-name", "missing-name", "download-failed", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			want := map[string]string{"TOKEN": "synthetic\nsecret with 'quotes'", "URL": "http://localhost:3000"}
			var requestID string
			var payload []byte
			var deleted bool
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			api := func(ctx context.Context, method, path string, body any) ([]byte, error) {
				switch {
				case method == "POST" && strings.HasSuffix(path, "/ghenv.yml/dispatches"):
					request := body.(map[string]any)
					if request["return_run_details"] != true || request["ref"] != "main" {
						t.Fatal("incorrect dispatch")
					}
					inputs := request["inputs"].(map[string]string)
					if inputs["environment"] != "development" || inputs["secret_names"] != "TOKEN" || inputs["variable_names"] != "URL" {
						t.Fatal("incorrect inputs")
					}
					requestID = inputs["request_id"]
					if !requestName.MatchString(requestID) {
						t.Fatal("invalid request ID")
					}
					values := map[string]string{"TOKEN": want["TOKEN"], "URL": want["URL"]}
					if mode == "extra-name" {
						values["UNREQUESTED"] = "should reject"
					}
					if mode == "missing-name" {
						delete(values, "TOKEN")
					}
					var err error
					payload, err = seal(inputs["public_key"], values, requestID)
					if err != nil {
						t.Fatal(err)
					}
					if mode == "tampered" {
						payload = []byte(`{"version":"bad"}`)
					}
					return []byte(`{"workflow_run_id":42}`), nil
				case method == "GET" && strings.HasSuffix(path, "/runs/42"):
					if mode == "failed-run" {
						return []byte(`{"status":"completed","conclusion":"failure"}`), nil
					}
					if mode == "cancelled" {
						cancel()
						return []byte(`{"status":"queued"}`), nil
					}
					return []byte(`{"status":"completed","conclusion":"success"}`), nil
				case method == "GET" && strings.Contains(path, "/runs/42/artifacts"):
					if mode == "missing-artifact" {
						return []byte(`{"artifacts":[]}`), nil
					}
					return []byte(fmt.Sprintf(`{"artifacts":[{"id":7,"name":"ghenv-%s","expired":false}]}`, requestID)), nil
				case method == "GET" && strings.HasSuffix(path, "/artifacts/7/zip"):
					if mode == "download-failed" {
						return nil, errors.New("network failed")
					}
					return makeZip(t, "payload.json", payload), nil
				case method == "DELETE" && strings.HasSuffix(path, "/artifacts/7"):
					if ctx.Err() != nil {
						t.Fatal("cleanup reused cancelled context")
					}
					deleted = true
					return nil, nil
				default:
					t.Fatalf("unexpected API call %s %s", method, path)
					return nil, nil
				}
			}
			got, err := fetchEnvironment(ctx, api, options{Repo: "owner/repo", Environment: "development", Ref: "main", Secrets: "TOKEN", Variables: "URL"})
			if mode == "success" {
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("fetch failed: %v", err)
				}
			} else if err == nil {
				t.Fatal("expected rejection")
			}
			wantDelete := mode != "failed-run" && mode != "missing-artifact" && mode != "cancelled"
			if deleted != wantDelete {
				t.Fatalf("artifact deleted=%v, want %v", deleted, wantDelete)
			}
		})
	}
}

func TestArtifactValidation(t *testing.T) {
	for _, data := range [][]byte{[]byte("not a zip"), makeZip(t, "../payload.json", []byte("payload")), makeZip(t, "payload.json", bytes.Repeat([]byte("a"), maxPayload*2+1))} {
		if _, err := readArtifact(data); err == nil {
			t.Fatal("accepted invalid artifact")
		}
	}
}

func TestOptions(t *testing.T) {
	valid := []string{"--repo", "owner/repo", "--secrets", "TOKEN", "--", "echo", "hello world", "--flag"}
	o, err := parseOptions(valid)
	if err != nil || len(o.Command) != 3 || o.Command[1] != "hello world" {
		t.Fatalf("parsing failed: %v", err)
	}
	for _, args := range [][]string{
		{"--repo", "owner/repo", "--secrets", "TOKEN"},
		{"--repo", "../repo", "--secrets", "TOKEN", "--", "echo"},
		{"--repo", "owner/repo", "--timeout", "0s", "--secrets", "TOKEN", "--", "echo"},
		{"--repo", "owner/repo", "--", "echo"},
		{"--repo", "owner/repo", "--secrets", "TOKEN", "surprise", "--", "echo"},
	} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("accepted invalid arguments %v", args)
		}
	}
}

func TestLaunch(t *testing.T) {
	// Re-enter the test executable as an actual child. No shell quoting or files.
	if os.Getenv("GHENV_TEST_CHILD") == "yes" {
		if os.Getenv("GHENV_TEST_VALUE") != "line one\n'quoted' $()=😀" || os.Getenv("GHENV_TEST_EMPTY") != "" {
			os.Exit(9)
		}
		os.Exit(23)
	}
	t.Setenv("GHENV_TEST_VALUE", "old value")
	t.Setenv("GHENV_TEST_EMPTY", "old value")
	err := launch([]string{os.Args[0], "-test.run=^TestLaunch$"}, map[string]string{
		"GHENV_TEST_CHILD": "yes", "GHENV_TEST_VALUE": "line one\n'quoted' $()=😀", "GHENV_TEST_EMPTY": "",
	})
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("child environment or exit status not preserved: %v", err)
	}
	if os.Getenv("GHENV_TEST_VALUE") != "old value" {
		t.Fatal("parent environment was modified")
	}
}

func TestSealCommand(t *testing.T) {
	key, public := keyPair(t)
	requestID := "0123456789abcdef0123456789abcdef"
	cmd := exec.Command(os.Args[0], "-test.run=^TestSealHelper$")
	cmd.Env = commandEnvironment(os.Environ(), map[string]string{
		"GHENV_SEAL_TEST_CHILD": "yes", "GHENV_SECRETS_JSON": `{"TOKEN":"synthetic\\nsecret"}`,
		"GHENV_VARS_JSON": `{}`, "GHENV_SECRET_NAMES": "TOKEN", "GHENV_VAR_NAMES": "",
		"GHENV_PUBLIC_KEY": public, "GHENV_REQUEST_ID": requestID,
	})
	payload, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	got, err := unseal(key, payload, requestID)
	if err != nil || got["TOKEN"] != "synthetic\\nsecret" {
		t.Fatalf("seal workflow contract failed: %v", err)
	}
}

func TestSealHelper(t *testing.T) {
	if os.Getenv("GHENV_SEAL_TEST_CHILD") != "yes" {
		return
	}
	os.Args = []string{"ghenv", "seal"}
	main()
	os.Exit(0)
}

// Exercise the complete CLI, including the real gh subprocess adapter and an
// actual child command. Only the GitHub service is substituted.
func TestCLI(t *testing.T) {
	dir := t.TempDir()
	shim := "#!/bin/sh\nexec \"$GHENV_TEST_EXECUTABLE\" -test.run=^TestFakeGH$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCLIHelper$", "--", "run", "--repo", "owner/repo",
		"--secrets", "GHENV_TEST_CHILD,GHENV_TEST_VALUE", "--vars", "GHENV_TEST_EMPTY", "--",
		os.Args[0], "-test.run=^TestLaunch$")
	cmd.Env = commandEnvironment(os.Environ(), map[string]string{
		"PATH":                  dir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"GHENV_TEST_EXECUTABLE": os.Args[0], "GHENV_TEST_STATE": dir,
		"GHENV_CLI_TEST": "yes",
	})
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("CLI failed: %v\n%s", err, output)
	}
	if bytes.Contains(output, []byte("line one")) {
		t.Fatal("CLI printed secret")
	}
	if _, err := os.Stat(filepath.Join(dir, "deleted")); err != nil {
		t.Fatal("CLI did not delete artifact")
	}
}

func TestCLIHelper(t *testing.T) {
	if os.Getenv("GHENV_CLI_TEST") != "yes" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"ghenv"}, os.Args[i+1:]...)
			break
		}
	}
	main()
	os.Exit(0)
}

func TestFakeGH(t *testing.T) {
	if os.Getenv("GHENV_CLI_TEST") != "yes" {
		return
	}
	stateFile := filepath.Join(os.Getenv("GHENV_TEST_STATE"), "public-request.json")
	var method, endpoint string
	for i, arg := range os.Args {
		if arg == "--method" {
			method, endpoint = os.Args[i+1], os.Args[i+2]
			break
		}
	}
	switch {
	case method == "POST":
		var request struct {
			ReturnRunDetails bool              `json:"return_run_details"`
			Inputs           map[string]string `json:"inputs"`
		}
		if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !request.ReturnRunDetails {
			t.Fatal("missing return_run_details")
		}
		data, _ := json.Marshal(request.Inputs)
		if err := os.WriteFile(stateFile, data, 0600); err != nil {
			t.Fatal(err)
		}
		fmt.Print(`{"workflow_run_id":42}`)
	case method == "DELETE":
		if err := os.WriteFile(filepath.Join(os.Getenv("GHENV_TEST_STATE"), "deleted"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	case strings.HasSuffix(endpoint, "/runs/42"):
		fmt.Print(`{"status":"completed","conclusion":"success"}`)
	default:
		data, err := os.ReadFile(stateFile)
		if err != nil {
			t.Fatal(err)
		}
		var inputs map[string]string
		if err := json.Unmarshal(data, &inputs); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(endpoint, "/runs/42/artifacts") {
			fmt.Printf(`{"artifacts":[{"id":7,"name":"ghenv-%s"}]}`, inputs["request_id"])
		} else if strings.HasSuffix(endpoint, "/artifacts/7/zip") {
			values := map[string]string{"GHENV_TEST_CHILD": "yes", "GHENV_TEST_VALUE": "line one\n'quoted' $()=😀", "GHENV_TEST_EMPTY": ""}
			payload, err := seal(inputs["public_key"], values, inputs["request_id"])
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stdout.Write(makeZip(t, "payload.json", payload)); err != nil {
				t.Fatal(err)
			}
		} else {
			t.Fatalf("unexpected endpoint %s", endpoint)
		}
	}
	os.Exit(0)
}
