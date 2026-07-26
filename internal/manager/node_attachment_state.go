package manager

type nodeAttachmentKey struct {
	nodeID       string
	attachmentID string
}

type nodeControlAttachmentLocalState struct {
	AttachmentID    string `json:"attachment_id"`
	State           string `json:"state"`
	BytesDownloaded int64  `json:"bytes_downloaded,omitempty"`
	TotalBytes      int64  `json:"total_bytes,omitempty"`
	SizeBytes       int64  `json:"size_bytes,omitempty"`
	LocalURI        string `json:"local_uri,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
	ErrorCode       string `json:"error_code,omitempty"`
	ErrorMessage    string `json:"error_message,omitempty"`
}

func (h *NodeControlHub) ObserveAttachmentState(
	nodeID string,
	state nodeControlAttachmentLocalState,
) {
	if h == nil || nodeID == "" || state.AttachmentID == "" {
		return
	}
	key := nodeAttachmentKey{nodeID: nodeID, attachmentID: state.AttachmentID}
	h.mu.Lock()
	if h.attachmentStates == nil {
		h.attachmentStates = make(map[nodeAttachmentKey]nodeControlAttachmentLocalState)
	}
	h.attachmentStates[key] = state
	h.mu.Unlock()
}

func (h *NodeControlHub) AttachmentState(
	nodeID string,
	attachmentID string,
) (nodeControlAttachmentLocalState, bool) {
	if h == nil {
		return nodeControlAttachmentLocalState{}, false
	}
	h.mu.RLock()
	state, ok := h.attachmentStates[nodeAttachmentKey{
		nodeID:       nodeID,
		attachmentID: attachmentID,
	}]
	h.mu.RUnlock()
	return state, ok
}
