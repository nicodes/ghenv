use std::process::Command;
#[test]
fn finite_version_and_refused_launch_produce_one_machine_result() {
    let binary = env!("CARGO_BIN_EXE_ghenv");
    let output = Command::new(binary)
        .args(["--json", "version"])
        .output()
        .unwrap();
    assert!(output.status.success());
    let value: serde_json::Value = serde_json::from_slice(&output.stdout).unwrap();
    assert_eq!(value["ok"], true);
    assert!(value["result"]["version"].is_string());
    let output = Command::new(binary)
        .args(["--json", "local", "--", "a-child-that-must-not-launch"])
        .output()
        .unwrap();
    assert!(!output.status.success());
    let value: serde_json::Value = serde_json::from_slice(&output.stdout).unwrap();
    assert_eq!(value["ok"], false);
    assert!(
        value["error"]["message"]
            .as_str()
            .unwrap()
            .contains("child's streams")
    );
}
