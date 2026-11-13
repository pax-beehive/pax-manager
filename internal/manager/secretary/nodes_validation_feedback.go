package secretary

import "github.com/pax-beehive/pax-manager/internal/manager/domain"

type nodePromptValidationFeedback struct {
	Code string `json:"code"`
	Hint string `json:"hint"`
}

func nodeValidationFeedback(job domain.SecretaryJob) *nodePromptValidationFeedback {
	feedback := legacyNodeValidationFeedback(job)
	if feedback == nil || !domain.SecretarySourceOnlyOutput(job.OutputPolicyVersion) {
		return feedback
	}
	switch feedback.Code {
	case "source_binding_capacity":
		return &nodePromptValidationFeedback{
			Code: feedback.Code,
			Hint: "Keep the entire bound publication within the stated budget, including exact source references and processing outcomes. Condense repeated prose and retain representative sources for useful conclusions.",
		}
	case "quote_not_visible", "source_unavailable":
		return &nodePromptValidationFeedback{
			Code: "source_unavailable",
			Hint: "Use supplied source IDs, unchanged baseline coverage, or exact additional sources exposed by an authorized read. Read a missing original or current summary baseline before using it; keep exact source versions and read receipts.",
		}
	case "schema":
		return feedback
	default:
		return &nodePromptValidationFeedback{
			Code: feedback.Code,
			Hint: "Correct source IDs, selected coverage, exact versions and read receipts using only supplied input, unchanged baseline sources or verified original reads.",
		}
	}
}

func legacyNodeValidationFeedback(job domain.SecretaryJob) *nodePromptValidationFeedback {
	feedback := genericNodeValidationFeedback(job.ValidationFeedback)
	attempt := job.Attempt
	if attempt == nil || attempt.State != "invalid" || attempt.NotDispatched ||
		job.BatchID == "" || attempt.BatchID != job.BatchID ||
		job.AttemptID == "" || attempt.ID != job.AttemptID {
		return feedback
	}
	// Persisted errors may contain untrusted text. Only exact fixed errors select
	// more specific instructions; the original error is never included.
	switch attempt.Error {
	case "secretary batch publication exceeds 64 KiB after source binding",
		"secretary batch publication exceeds 64 KiB after source binding: conflict",
		"summary batch exceeds the 64 KiB budget: conflict":
		return &nodePromptValidationFeedback{
			Code: "source_binding_capacity",
			Hint: "Keep the entire bound publication within the stated budget, including source references and evidence metadata. Shorten repeated prose and quotes; retain every reference needed to support retained conclusions.",
		}
	case "secretary evidence was not present in the supplied or verified text",
		"secretary evidence quote is empty":
		return &nodePromptValidationFeedback{
			Code: "quote_not_visible",
			Hint: "Copy an exact quote from supplied text or a verified read. The default assistant view contains only its last paragraph. Read or expand the exact message before quoting other paragraphs, and include the returned read receipt.",
		}
	case "summary selected an unavailable source message",
		"summary baseline is unavailable",
		"summary revision requires its exact read baseline",
		"related source was not exposed by a verified read",
		"secretary evidence reference is invalid or conflicting",
		"secretary evidence does not match its trusted version or view":
		return &nodePromptValidationFeedback{
			Code: "source_unavailable",
			Hint: "Use only supplied sources or sources exposed by an authorized read. Read missing messages or the exact current summary baseline before using them. Preserve exact message versions, views, and returned read receipts.",
		}
	default:
		return feedback
	}
}

func genericNodeValidationFeedback(category string) *nodePromptValidationFeedback {
	switch category {
	case "schema":
		return &nodePromptValidationFeedback{
			Code: "schema",
			Hint: "Return exactly the stated JSON schema with one outcome for every changed or removed message. Keep supported content within the stated publication limits.",
		}
	case "evidence":
		return &nodePromptValidationFeedback{
			Code: "evidence",
			Hint: "Correct references, coverage, and exact evidence quotes using only supplied text or verified reads. Preserve exact source versions and read receipts.",
		}
	default:
		return nil
	}
}
