package keys

import "testing"

func TestIsStatefulKeyIncludesFrost(t *testing.T) {
	t.Parallel()

	cases := []string{
		KeyFrostConfig(),
		KeyFrostVaultState("BTC", 1),
		KeyFrostWithdraw("wid-1"),
		KeyFrostSignedPackage("job-1", 0),
	}

	for _, key := range cases {
		if !IsStatefulKey(key) {
			t.Fatalf("expected stateful key: %s", key)
		}
	}
}

func TestIsStatefulKeyExcludesKVOnlyFlowKeys(t *testing.T) {
	t.Parallel()

	cases := []string{
		KeyTxRaw("tx-1"),
		KeyAnyTx("tx-1"),
		KeyPendingAnyTx("tx-1"),
		KeyVMCommitHeight(1),
		KeyVMAppliedTx("tx-1"),
	}

	for _, key := range cases {
		if IsStatefulKey(key) {
			t.Fatalf("expected KV-only key: %s", key)
		}
	}
}

func TestStateDBBusinessKeySpecsContainsFrostPrefix(t *testing.T) {
	t.Parallel()

	specs := StateDBBusinessKeySpecs()
	want := withVer("frost_")
	for _, spec := range specs {
		if spec.Prefix == want {
			return
		}
	}
	t.Fatalf("expected StateDB business key specs to contain prefix %q", want)
}
