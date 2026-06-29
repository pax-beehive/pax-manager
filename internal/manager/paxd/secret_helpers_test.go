package paxd

import "testing"

func TestSecretHelpers(t *testing.T) {
	if got := defaultSecretVersion(" "); got != "latest" {
		t.Fatalf("default secret version = %q, want latest", got)
	}
	if got := defaultSecretVersion(" version:2 "); got != "version:2" {
		t.Fatalf("trimmed secret version = %q, want version:2", got)
	}
	if got := secretResourceRef("secret_1", ""); got != "secret_1" {
		t.Fatalf("resource ref = %q, want secret_1", got)
	}
	if got := secretResourceRef("secret_1", "version:2"); got != "secret_1@version:2" {
		t.Fatalf("versioned resource ref = %q", got)
	}
	if secretApprovalTitle("write_value") != "Write secret vault value" {
		t.Fatal("write approval title did not describe write")
	}
	if secretApprovalTitle("read_value") != "Read secret vault value" {
		t.Fatal("read approval title did not describe read")
	}
	if got := secretApprovalDescription("write_value", "secret_1", ""); got == "" ||
		got == secretApprovalDescription("read_value", "secret_1", "latest") {
		t.Fatalf("approval descriptions are not operation-specific: %q", got)
	}
}

func TestSecretActionFingerprintIsStableAndScoped(t *testing.T) {
	first := secretActionFingerprint("read_value", "secret_1", "latest", "agent_1", "sess_1")
	second := secretActionFingerprint("read_value", "secret_1", "latest", "agent_1", "sess_1")
	if first == "" || first != second {
		t.Fatalf("fingerprint first=%q second=%q", first, second)
	}
	otherSession := secretActionFingerprint("read_value", "secret_1", "latest", "agent_1", "sess_2")
	if otherSession == first {
		t.Fatal("fingerprint did not include session scope")
	}
	otherOperation := secretActionFingerprint(
		"write_value",
		"secret_1",
		"latest",
		"agent_1",
		"sess_1",
	)
	if otherOperation == first {
		t.Fatal("fingerprint did not include operation")
	}
}
