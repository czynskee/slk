package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/slack-go/slack"

	"github.com/gammons/slk/internal/cache"
	"github.com/gammons/slk/internal/core"
	"github.com/gammons/slk/internal/ui"
	"github.com/gammons/slk/internal/ui/sidebar"
)

// orgFallbackConversationResolver retries a member workspace's failed
// conversation lookup through the same-user org session, so a grid
// conversation the org workspace owns still gets its sidebar row where
// the member session received the messages. The own resolver's
// success, the org fallback's success, and the no-org passthrough of
// the own failure are all pinned - and the org resolver returning the
// own error (what the wired closure does when it cannot do better)
// keeps the caller's retry-backoff classification intact.
func TestOrgFallbackConversationResolver(t *testing.T) {
	ownErr := errors.New("method_not_supported_for_this_token")
	channel := func(id string) *slack.Channel {
		return &slack.Channel{GroupConversation: slack.GroupConversation{
			Conversation: slack.Conversation{ID: id},
		}}
	}
	orgCh := channel("D1")
	calls := 0
	own := func(ctx context.Context, channelID string) (*slack.Channel, error) {
		calls++
		if channelID == "C-own" {
			return channel("C-own"), nil
		}
		return nil, ownErr
	}
	orgCalls := 0
	org := func(ctx context.Context, channelID string, ownErr error) (*slack.Channel, error) {
		orgCalls++
		if ownErr == nil {
			t.Fatal("the org resolver was called without the own failure")
		}
		return orgCh, nil
	}

	// No org session: the own resolver alone, failures included.
	passthrough := orgFallbackConversationResolver(own, nil)
	if ch, err := passthrough(context.Background(), "D1"); ch != nil || err != ownErr {
		t.Fatalf("no-org passthrough = (%v, %v); want the own failure", ch, err)
	}

	resolve := orgFallbackConversationResolver(own, org)

	// The own resolver's answer wins without consulting the org.
	ch, err := resolve(context.Background(), "C-own")
	if err != nil || ch.ID != "C-own" {
		t.Fatalf("own-success path = (%v, %v)", ch, err)
	}
	if orgCalls != 0 {
		t.Fatal("the org resolver ran although the own resolver succeeded")
	}

	// The own resolver's failure falls through to the org session.
	ch, err = resolve(context.Background(), "D1")
	if err != nil || ch != orgCh {
		t.Fatalf("fallback path = (%v, %v); want the org session's channel", ch, err)
	}
	if calls != 3 || orgCalls != 1 {
		t.Fatalf("calls = own:%d org:%d; want own:3 org:1 (passthrough, own-success, fallback attempt)", calls, orgCalls)
	}

	// The wired closure's shape: when the org resolver returns the own
	// error (no org session, or the org lookup failed too), the caller
	// sees that error, not a wrapper one - the backoff classifies it.
	giveUp := orgFallbackConversationResolver(own, func(ctx context.Context, channelID string, ownErr error) (*slack.Channel, error) {
		return nil, ownErr
	})
	if _, err := giveUp(context.Background(), "D1"); err != ownErr {
		t.Fatalf("own-error passthrough = %v; want the own error itself", err)
	}
}

// closeHandler is a handler for T1, active, holding general (C1),
// random (C2) and a DM with U1 (D1) in every place a conversation is
// kept: both workspace snapshots, the conversation set, the notifier
// maps and the cache. It returns the messages sent to the UI.
func closeHandler(t *testing.T) (*rtmEventHandler, *captureSender) {
	t.Helper()
	wctx := &WorkspaceContext{}
	h := &rtmEventHandler{
		wsCtx:        wctx,
		workspaceID:  "T1",
		db:           newTestDB(t),
		isActive:     func() bool { return true },
		channelNames: map[string]string{},
		channelTypes: map[string]string{},
	}
	for _, it := range []sidebar.ChannelItem{
		{ID: "C1", Name: "general", Type: "channel"},
		{ID: "C2", Name: "random", Type: "channel"},
		{ID: "D1", Name: "alice", Type: "dm", DMUserID: "U1"},
	} {
		wctx.Channels = append(wctx.Channels, it)
		wctx.FinderItems = append(wctx.FinderItems, core.ChannelFinderItem{ID: it.ID, Name: it.Name, Type: it.Type, Joined: true})
		wctx.MarkConversation(it.ID)
		h.rememberConversation(it)
		if err := h.db.UpsertChannel(cache.Channel{ID: it.ID, WorkspaceID: "T1", Name: it.Name, Type: it.Type, IsMember: true}); err != nil {
			t.Fatalf("UpsertChannel: %v", err)
		}
	}
	sender := &captureSender{}
	h.program = sender
	return h, sender
}

func channelIDs(items []sidebar.ChannelItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

// A closed conversation leaves every place the workspace keeps it, and
// the UI hears of it once: the echo of a removal already applied sends
// nothing.
func TestOnConversationClosed_RemovesTheConversation(t *testing.T) {
	h, sender := closeHandler(t)
	// What a workspace switch handed to the UI before the removal.
	handedChannels, handedFinder := h.wsCtx.Channels, h.wsCtx.FinderItems

	h.OnConversationClosed("C2")
	h.OnConversationClosed("C2")

	if want := []string{"C1", "D1"}; !slices.Equal(channelIDs(h.wsCtx.Channels), want) {
		t.Errorf("Channels = %v, want %v", channelIDs(h.wsCtx.Channels), want)
	}
	if slices.ContainsFunc(h.wsCtx.FinderItems, func(it core.ChannelFinderItem) bool { return it.ID == "C2" }) {
		t.Errorf("FinderItems = %+v, want C2 gone", h.wsCtx.FinderItems)
	}
	if h.wsCtx.HasConversation("C2") {
		t.Error("HasConversation(C2) = true, want false")
	}
	if cached, err := h.db.GetChannel("C2"); err != nil || cached.IsMember {
		t.Errorf("cached C2 = %+v, %v; want is_member cleared", cached, err)
	}
	if want := []tea.Msg{ui.ConversationClosedMsg{TeamID: "T1", ChannelID: "C2"}}; !slices.Equal(sender.sent, want) {
		t.Errorf("sent = %+v, want %+v", sender.sent, want)
	}
	if want := []string{"C1", "C2", "D1"}; !slices.Equal(channelIDs(handedChannels), want) {
		t.Errorf("the Channels slice handed out earlier = %v, want %v untouched", channelIDs(handedChannels), want)
	}
	if len(handedFinder) != 3 || handedFinder[1].ID != "C2" || handedFinder[2].ID != "D1" {
		t.Errorf("the FinderItems slice handed out earlier = %+v, want it untouched", handedFinder)
	}
}

func TestOnConversationClosed_InactiveWorkspaceSendsNothing(t *testing.T) {
	h, sender := closeHandler(t)
	h.isActive = func() bool { return false }
	h.OnConversationClosed("C2")
	if want := []string{"C1", "D1"}; !slices.Equal(channelIDs(h.wsCtx.Channels), want) {
		t.Errorf("Channels = %v, want %v", channelIDs(h.wsCtx.Channels), want)
	}
	if len(sender.sent) != 0 {
		t.Errorf("sent = %+v, want nothing for an inactive workspace", sender.sent)
	}
}

// Slack can post a "left the channel" notice in a channel the user leaves.
// That message must not bring the row back; a message in a closed DM
// must.
func TestOnConversationClosed_OnlyAClosedDMComesBackOnAMessage(t *testing.T) {
	h, _ := closeHandler(t)
	var lookups []string
	h.resolveConversation = func(_ context.Context, id string) (*slack.Channel, error) {
		lookups = append(lookups, id)
		return &slack.Channel{GroupConversation: slack.GroupConversation{
			Conversation: slack.Conversation{ID: id, IsIM: true, User: "U1"},
		}}, nil
	}
	h.OnConversationClosed("C2")
	h.OnConversationClosed("D1")

	if _, _, added := h.discoverConversation("C2"); added {
		t.Error("a message brought back the channel the user left")
	}
	if _, _, added := h.discoverConversation("D1"); !added {
		t.Error("a message did not bring back the closed DM")
	}
	if want := []string{"D1"}; !slices.Equal(lookups, want) {
		t.Errorf("conversations.info ran for %v, want %v", lookups, want)
	}
	if want := []string{"C1", "D1"}; !slices.Equal(channelIDs(h.wsCtx.Channels), want) {
		t.Errorf("Channels = %v, want %v", channelIDs(h.wsCtx.Channels), want)
	}
}

// A leave Slack accepted is applied on the event owner even when no
// echo arrives.
func TestQueueConversationClosed_DrainsOnPendingEvents(t *testing.T) {
	h, sender := closeHandler(t)
	h.starWake = make(chan struct{}, 1)

	h.queueConversationClosed("C2")
	select {
	case <-h.PendingEvents():
	default:
		t.Fatal("queueing a closed conversation did not wake the event owner")
	}
	if len(h.wsCtx.Channels) != 3 {
		t.Fatalf("Channels = %v before the drain, want them untouched off the event owner", channelIDs(h.wsCtx.Channels))
	}
	h.OnPendingEvents()

	if want := []string{"C1", "D1"}; !slices.Equal(channelIDs(h.wsCtx.Channels), want) {
		t.Errorf("Channels = %v, want %v", channelIDs(h.wsCtx.Channels), want)
	}
	if want := []tea.Msg{ui.ConversationClosedMsg{TeamID: "T1", ChannelID: "C2"}}; !slices.Equal(sender.sent, want) {
		t.Errorf("sent = %+v, want %+v", sender.sent, want)
	}
}
