package manager

import (
	"errors"
	"sync"
)

var errACPSSESubscriberOverflow = errors.New("ACP session stream fell behind; reconnect to resync")

type acpResponseWaiter struct {
	managerSessionID string
	requestKind      string
	ch               chan []byte
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
}

func newACPSessionMux() *acpSessionMux {
	return &acpSessionMux{
		waiters:     make(map[string]*acpResponseWaiter),
		workerCalls: make(map[string]acpWorkerRequest),
		subscribers: make(map[string]map[*acpSSESubscriber]struct{}),
	}
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
) (<-chan []byte, func()) {
	waiter := &acpResponseWaiter{
		managerSessionID: managerSessionID,
		requestKind:      requestKind,
		ch:               make(chan []byte, 1),
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
	case waiter.ch <- append([]byte(nil), payload...):
	default:
	}
	return true
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
