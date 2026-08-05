package manager

import (
	"errors"
	"sync"
)

var errACPSSESubscriberOverflow = errors.New("ACP session stream fell behind; reconnect to resync")

type acpResponseWaiter struct {
	managerSessionID string
	requestKind      string
	ch               chan acpResponseWaiterResult
}

type acpResponseWaiterResult struct {
	payload []byte
	err     error
}

type acpWorkerRequest struct {
	managerSessionID string
	nativeSessionID  string
	requestKind      string
}

type acpSessionMux struct {
	mu          sync.Mutex
	waiters     map[string]*acpResponseWaiter
	workerCalls map[string]acpWorkerRequest
	subscribers map[string]map[*acpSSESubscriber]struct{}
	activeTurns map[string]*acpTurnAdmission
}

type acpTurnAdmission struct{ _ byte }

func newACPSessionMux() *acpSessionMux {
	return &acpSessionMux{
		waiters:     make(map[string]*acpResponseWaiter),
		workerCalls: make(map[string]acpWorkerRequest),
		subscribers: make(map[string]map[*acpSSESubscriber]struct{}),
		activeTurns: make(map[string]*acpTurnAdmission),
	}
}

func (m *acpSessionMux) admitTurn(sessionID string) (*acpTurnAdmission, bool) {
	if sessionID == "" {
		return nil, false
	}
	token := &acpTurnAdmission{}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeTurns[sessionID] != nil {
		return nil, false
	}
	m.activeTurns[sessionID] = token
	return token, true
}

func (m *acpSessionMux) releaseTurn(sessionID string, token *acpTurnAdmission) {
	if sessionID == "" || token == nil {
		return
	}
	m.mu.Lock()
	if m.activeTurns[sessionID] == token {
		delete(m.activeTurns, sessionID)
	}
	m.mu.Unlock()
}

func (m *acpSessionMux) hasActiveTurns() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.activeTurns) > 0
}

func (m *acpSessionMux) activeTurnCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.activeTurns)
}

func (m *acpSessionMux) trackWorkerRequest(
	requestID string,
	managerSessionID string,
	nativeSessionID string,
	requestKind string,
) {
	if requestID == "" || managerSessionID == "" || nativeSessionID == "" {
		return
	}
	m.mu.Lock()
	m.workerCalls[workerRequestKey(managerSessionID, requestID)] = acpWorkerRequest{
		managerSessionID: managerSessionID,
		nativeSessionID:  nativeSessionID,
		requestKind:      requestKind,
	}
	m.mu.Unlock()
}

func (m *acpSessionMux) workerRequestContext(
	requestID string,
	managerSessionID string,
	allowUnambiguousFallback bool,
) (acpWorkerRequest, bool) {
	if requestID == "" {
		return acpWorkerRequest{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	request, ok := m.workerCalls[workerRequestKey(managerSessionID, requestID)]
	if ok || !allowUnambiguousFallback {
		return request, ok
	}
	var matched acpWorkerRequest
	found := false
	for key, candidate := range m.workerCalls {
		if key != workerRequestKey(candidate.managerSessionID, requestID) {
			continue
		}
		if found {
			return acpWorkerRequest{}, false
		}
		matched = candidate
		found = true
	}
	return matched, found
}

func (m *acpSessionMux) completeWorkerRequest(requestID string, request acpWorkerRequest) {
	if requestID == "" {
		return
	}
	m.mu.Lock()
	key := workerRequestKey(request.managerSessionID, requestID)
	if m.workerCalls[key] == request {
		delete(m.workerCalls, key)
	}
	m.mu.Unlock()
}

func workerRequestKey(managerSessionID string, requestID string) string {
	return managerSessionID + "\x00" + requestID
}

func (m *acpSessionMux) addResponseWaiter(
	requestID string,
	managerSessionID string,
	requestKind string,
) (<-chan acpResponseWaiterResult, func()) {
	waiter := &acpResponseWaiter{
		managerSessionID: managerSessionID,
		requestKind:      requestKind,
		ch:               make(chan acpResponseWaiterResult, 1),
	}
	m.mu.Lock()
	m.waiters[requestID] = waiter
	m.mu.Unlock()
	return waiter.ch, func() {
		m.mu.Lock()
		if m.waiters[requestID] == waiter {
			delete(m.waiters, requestID)
		}
		m.mu.Unlock()
	}
}

func (m *acpSessionMux) responseContext(requestID string) (string, string, bool) {
	if requestID == "" {
		return "", "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	waiter := m.waiters[requestID]
	if waiter == nil {
		return "", "", false
	}
	return waiter.managerSessionID, waiter.requestKind, true
}

func (m *acpSessionMux) notifyResponseWaiter(requestID string, payload []byte) bool {
	if requestID == "" {
		return false
	}
	m.mu.Lock()
	waiter := m.waiters[requestID]
	if waiter != nil {
		delete(m.waiters, requestID)
	}
	m.mu.Unlock()
	if waiter == nil {
		return false
	}
	select {
	case waiter.ch <- acpResponseWaiterResult{payload: append([]byte(nil), payload...)}:
	default:
	}
	return true
}

// interruptSession wakes request owners and stream subscribers after the
// authoritative runtime snapshot reports that their session has no active
// turn. Other sessions sharing the same physical tunnel remain untouched.
func (m *acpSessionMux) interruptSession(sessionID string, err error) int {
	if m == nil || sessionID == "" || err == nil {
		return 0
	}
	m.mu.Lock()
	waiters := make([]*acpResponseWaiter, 0)
	for requestID, waiter := range m.waiters {
		if waiter.managerSessionID != sessionID {
			continue
		}
		delete(m.waiters, requestID)
		waiters = append(waiters, waiter)
	}
	// A response removes its waiter before the request owner consumes it. In
	// that small window an idle snapshot is expected and must not replace the
	// already-buffered successful response with an interruption.
	subscribers := make([]*acpSSESubscriber, 0)
	if len(waiters) > 0 {
		subscribers = make([]*acpSSESubscriber, 0, len(m.subscribers[sessionID]))
		for sub := range m.subscribers[sessionID] {
			subscribers = append(subscribers, sub)
		}
		delete(m.subscribers, sessionID)
	}
	m.mu.Unlock()

	for _, waiter := range waiters {
		select {
		case waiter.ch <- acpResponseWaiterResult{err: err}:
		default:
		}
	}
	for _, sub := range subscribers {
		sub.close(err)
	}
	return len(waiters) + len(subscribers)
}

func (m *acpSessionMux) subscribe(sessionID string) *acpSSESubscriber {
	sub := &acpSSESubscriber{
		sessionID: sessionID,
		ch:        make(chan []byte, 64),
		terminal:  make(chan error, 1),
	}
	m.mu.Lock()
	if m.subscribers[sessionID] == nil {
		m.subscribers[sessionID] = make(map[*acpSSESubscriber]struct{})
	}
	m.subscribers[sessionID][sub] = struct{}{}
	m.mu.Unlock()
	return sub
}

func (m *acpSessionMux) unsubscribe(sub *acpSSESubscriber) {
	if sub == nil {
		return
	}
	m.mu.Lock()
	subs := m.subscribers[sub.sessionID]
	if _, ok := subs[sub]; ok {
		delete(subs, sub)
		if len(subs) == 0 {
			delete(m.subscribers, sub.sessionID)
		}
		sub.close(nil)
	}
	m.mu.Unlock()
}

func (m *acpSessionMux) publish(sessionID string, payload []byte) bool {
	if sessionID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	subs := m.subscribers[sessionID]
	delivered := false
	for sub := range subs {
		select {
		case sub.ch <- append([]byte(nil), payload...):
			delivered = true
		default:
			delete(subs, sub)
			sub.close(errACPSSESubscriberOverflow)
		}
	}
	if len(subs) == 0 {
		delete(m.subscribers, sessionID)
	}
	return delivered
}

func (m *acpSessionMux) counts() acpAsyncReceiverCounts {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, subs := range m.subscribers {
		count += len(subs)
	}
	return acpAsyncReceiverCounts{
		responseWaiters: len(m.waiters),
		sseSubscribers:  count,
	}
}
