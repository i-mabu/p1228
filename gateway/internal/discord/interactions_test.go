package discord

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestAutoDeferFailureDoesNotMarkInteractionAcknowledged(t *testing.T) {
	store := NewInteractionStore(nil)
	store.refs["ref"] = &interactionRef{BotID: "general", InteractionID: "interaction", Token: "token", ApplicationID: "app", Expires: time.Now().Add(time.Minute)}
	store.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	if err := store.AutoDefer(context.Background(), "general", "ref", 5); err == nil {
		t.Fatal("expected transport failure")
	}
	state := store.refs["ref"]
	if state.InitialSent || state.InitialPending || !state.InitialUnknown {
		t.Fatalf("unexpected state after failed defer: %+v", state)
	}
	if err := store.Do(context.Background(), "general", "ref", "followups", []byte(`{"content":"late"}`)); err == nil {
		t.Fatal("follow-up must not be sent when initial acknowledgement outcome is unknown")
	}
}

func TestInitialResponseFailureDoesNotCommitACKState(t *testing.T) {
	store := NewInteractionStore(nil)
	store.refs["ref"] = &interactionRef{BotID: "general", InteractionID: "interaction", Token: "token", ApplicationID: "app", Expires: time.Now().Add(time.Minute)}
	store.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	if err := store.Do(context.Background(), "general", "ref", "initial", []byte(`{"type":4,"data":{"content":"hello"}}`)); err == nil {
		t.Fatal("expected transport failure")
	}
	state := store.refs["ref"]
	if state.InitialSent || state.InitialPending || !state.InitialUnknown {
		t.Fatalf("unexpected state after failed initial response: %+v", state)
	}
	if err := store.Do(context.Background(), "general", "ref", "initial", []byte(`{"type":4,"data":{"content":"retry"}}`)); err == nil {
		t.Fatal("duplicate initial callback must be rejected after an unknown outcome")
	}
}

func TestAutoDeferSuccessCommitsACKState(t *testing.T) {
	store := NewInteractionStore(nil)
	store.refs["ref"] = &interactionRef{BotID: "general", InteractionID: "interaction", Token: "token", ApplicationID: "app", Expires: time.Now().Add(time.Minute)}
	store.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	if err := store.AutoDefer(context.Background(), "general", "ref", 5); err != nil {
		t.Fatal(err)
	}
	state := store.refs["ref"]
	if !state.InitialSent || state.InitialPending || state.InitialUnknown || !state.AutoDeferred || state.AutoResponseType != 5 {
		t.Fatalf("unexpected state after successful defer: %+v", state)
	}
}

func TestLateComponentMessageBecomesFollowupAfterAutoDefer(t *testing.T) {
	store := NewInteractionStore(nil)
	store.refs["ref"] = &interactionRef{
		BotID: "general", InteractionID: "interaction", Token: "token", ApplicationID: "app",
		Expires: time.Now().Add(time.Minute), InitialSent: true, AutoDeferred: true, AutoResponseType: 6,
	}
	requested := false
	store.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requested = true
		if request.Method != http.MethodPost || request.URL.Path != "/api/v10/webhooks/app/token" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"content":"Role granted","flags":64}` {
			t.Errorf("follow-up body = %s", body)
		}
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	if err := store.Do(context.Background(), "general", "ref", "initial", []byte(`{"type":4,"data":{"content":"Role granted","flags":64}}`)); err != nil {
		t.Fatal(err)
	}
	if !requested {
		t.Fatal("late component response was not sent as a follow-up")
	}
}

func TestLateComponentFollowupFailurePreservesDeferStateForRetry(t *testing.T) {
	store := NewInteractionStore(nil)
	store.refs["ref"] = &interactionRef{
		BotID: "general", InteractionID: "interaction", Token: "token", ApplicationID: "app",
		Expires: time.Now().Add(time.Minute), InitialSent: true, AutoDeferred: true, AutoResponseType: 6,
	}
	calls := 0
	store.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		status := http.StatusInternalServerError
		if calls == 2 {
			status = http.StatusNoContent
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	body := []byte(`{"type":4,"data":{"content":"retry me"}}`)
	if err := store.Do(context.Background(), "general", "ref", "initial", body); err == nil {
		t.Fatal("expected first follow-up attempt to fail")
	}
	state := store.refs["ref"]
	if !state.InitialSent || !state.AutoDeferred || state.AutoResponseType != 6 {
		t.Fatalf("failed follow-up consumed defer state: %+v", state)
	}
	if err := store.Do(context.Background(), "general", "ref", "initial", body); err != nil {
		t.Fatalf("retry after follow-up failure: %v", err)
	}
	if calls != 2 {
		t.Fatalf("request attempts=%d, want 2", calls)
	}
	if state.AutoDeferred || state.AutoResponseType != 0 {
		t.Fatalf("successful retry did not consume defer state: %+v", state)
	}
}
