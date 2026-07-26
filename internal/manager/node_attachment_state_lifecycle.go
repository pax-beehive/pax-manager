package manager

func (h *NodeControlHub) clearAttachmentStatesLocked(nodeID string) {
	for key := range h.attachmentStates {
		if key.nodeID == nodeID {
			delete(h.attachmentStates, key)
		}
	}
}
