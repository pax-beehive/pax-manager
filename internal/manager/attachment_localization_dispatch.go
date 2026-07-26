package manager

import (
	"context"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *Service) dispatchMissingAttachments(
	ctx context.Context,
	nodeID string,
	attachments []domain.UserAttachment,
) error {
	for _, attachment := range attachments {
		state, ok := s.nodeControls.AttachmentState(nodeID, attachment.AttachmentID)
		if ok && state.State == "ready" {
			continue
		}
		s.nodeControls.ObserveAttachmentState(nodeID, nodeControlAttachmentLocalState{
			AttachmentID: attachment.AttachmentID,
			State:        "queued",
		})
		err := s.dispatchEnsureAttachmentLocal(ctx, nodeID, attachment)
		if err != nil &&
			!errors.Is(err, context.DeadlineExceeded) &&
			!errors.Is(err, context.Canceled) {
			return err
		}
	}
	return nil
}
