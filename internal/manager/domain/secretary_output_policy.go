package domain

const (
	SecretaryLegacyOutputPolicyVersion     = "summary-claims-v1"
	SecretarySourceOnlyOutputPolicyVersion = "summary-sources-v2"
)

func SecretaryOutputPolicySupported(policy string) bool {
	return policy == "" || policy == SecretaryLegacyOutputPolicyVersion ||
		policy == SecretarySourceOnlyOutputPolicyVersion
}

func SecretarySourceOnlyOutput(policy string) bool {
	return policy == SecretarySourceOnlyOutputPolicyVersion
}
