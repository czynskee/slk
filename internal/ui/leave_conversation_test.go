package ui

import (
	"errors"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/gammons/slk/internal/core"
	"github.com/gammons/slk/internal/ids"
	"github.com/gammons/slk/internal/ui/sidebar"
)

// leaveCall is one ChannelService.Leave call.
type leaveCall struct{ teamID, channelID, channelType string }

// leaveApp is slashApp in T1 on C1 with general (C1), random (C2), the
// private channel secret (C3) and a DM with alice (D1) in the sidebar
// and the finder. Leave calls are recorded and answered with err (nil:
// Slack agrees, and the command delivers nothing). Lookup knows every
// conversation still in the sidebar, as the engine's does.
func leaveApp(t *testing.T, err error) (*App, *[]leaveCall) {
	t.Helper()
	a, _ := slashApp(t, nil)
	a.SetChannels([]sidebar.ChannelItem{
		{ID: "C1", Name: "general", Type: "channel"},
		{ID: "C2", Name: "random", Type: "channel"},
		{ID: "C3", Name: "secret", Type: "private"},
		{ID: "D1", Name: "alice", Type: "dm"},
	})
	a.channelFinder.SetItems([]core.ChannelFinderItem{
		{ID: "C1", Name: "general", Type: "channel", Joined: true},
		{ID: "C2", Name: "random", Type: "channel", Joined: true},
		{ID: "C3", Name: "secret", Type: "private", Joined: true},
		{ID: "D1", Name: "alice", Type: "dm", Joined: true},
	})
	calls := &[]leaveCall{}
	a.SetChannelService(core.NewChannelService(core.ChannelServiceFuncs{
		Leave: func(teamID ids.TeamID, channelID ids.ChannelID, channelType string) core.Cmd {
			return func() core.Msg {
				*calls = append(*calls, leaveCall{string(teamID), string(channelID), channelType})
				if err != nil {
					return ConversationLeaveFailedMsg{ChannelID: string(channelID), Err: err}
				}
				return nil
			}
		},
		Lookup: func(id ids.ChannelID) (string, string, bool) {
			for _, it := range a.sidebar.Items() {
				if it.ID == string(id) {
					return it.Name, it.Type, true
				}
			}
			return "", "", false
		},
	}))
	return a, calls
}

// typeIn makes channelID the conversation the channel composer belongs
// to, as selecting it does.
func typeIn(a *App, channelID string) {
	a.activeChannelID = channelID
	a.compose.SetDraftContext("T1", channelID, "")
}

func sidebarIDs(a *App) []string {
	var out []string
	for _, it := range a.sidebar.Items() {
		out = append(out, it.ID)
	}
	return out
}

func TestSlashLeave_LeavesTheConversationItWasTypedIn(t *testing.T) {
	cases := []struct {
		typed, channelID string
		want             leaveCall
	}{
		{"/leave", "C1", leaveCall{"T1", "C1", "channel"}},
		{"/part", "C2", leaveCall{"T1", "C2", "channel"}},
		{"/close", "D1", leaveCall{"T1", "D1", "dm"}},
	}
	for _, tc := range cases {
		t.Run(tc.typed, func(t *testing.T) {
			a, calls := leaveApp(t, nil)
			typeIn(a, tc.channelID)
			a.compose.SetValue(tc.typed)
			pressKeyAll(t, a, keyCode(tea.KeyEnter))
			if !slices.Equal(*calls, []leaveCall{tc.want}) {
				t.Fatalf("Leave calls = %+v, want [%+v]", *calls, tc.want)
			}
			if a.mode == ModeConfirm {
				t.Error("a prompt opened; only a private channel asks first")
			}
			// The row goes with ConversationClosedMsg, not with the call.
			if !slices.Contains(sidebarIDs(a), tc.channelID) {
				t.Errorf("sidebar = %v, want %s kept until Slack agrees", sidebarIDs(a), tc.channelID)
			}
		})
	}
}

func TestSlashLeave_FromTheThreadComposerLeavesTheThreadsChannel(t *testing.T) {
	a, calls := leaveApp(t, nil)
	showThread(t, a)
	a.threadCompose.SetDraftContext("T1", "C1", "10.0")
	a.threadCompose.SetValue("/leave")
	pressKeyAll(t, a, keyCode(tea.KeyEnter))
	if want := (leaveCall{"T1", "C1", "channel"}); !slices.Equal(*calls, []leaveCall{want}) {
		t.Fatalf("Leave calls = %+v, want [%+v]", *calls, want)
	}
}

func TestSlashLeave_PrivateChannelAsksFirst(t *testing.T) {
	a, calls := leaveApp(t, nil)
	typeIn(a, "C3")
	a.compose.SetValue("/leave")
	pressKeyAll(t, a, keyCode(tea.KeyEnter))
	if a.mode != ModeConfirm || !dialogShows(a, "secret") {
		t.Fatalf("mode = %v; want the prompt naming the channel", a.mode)
	}
	if len(*calls) != 0 {
		t.Fatalf("Leave calls = %+v before the prompt was confirmed", *calls)
	}
	pressKeyAll(t, a, keyPress('y'))
	if want := (leaveCall{"T1", "C3", "private"}); !slices.Equal(*calls, []leaveCall{want}) {
		t.Fatalf("Leave calls = %+v, want [%+v]", *calls, want)
	}
}

func TestSlashLeave_PrivateChannelPromptCancels(t *testing.T) {
	a, calls := leaveApp(t, nil)
	typeIn(a, "C3")
	a.compose.SetValue("/leave")
	pressKeyAll(t, a, keyCode(tea.KeyEnter))
	pressKeyAll(t, a, keyPress('n'))
	if a.mode == ModeConfirm {
		t.Fatal("the prompt is still open after n")
	}
	if len(*calls) != 0 {
		t.Errorf("Leave calls = %+v, want none after cancelling", *calls)
	}
}

func TestSlashLeave_ArgumentsAreRefused(t *testing.T) {
	a, calls := leaveApp(t, nil)
	a.compose.SetValue("/leave #random")
	_ = pressEnter(a)
	if !toastShows(a, "Usage: /leave") {
		t.Errorf("status bar = %q, want the usage", statusbarText(a))
	}
	if got := a.compose.Value(); got != "/leave #random" {
		t.Errorf("compose = %q, want the typed text back", got)
	}
	if len(*calls) != 0 {
		t.Errorf("Leave calls = %+v, want none: /leave takes no channel", *calls)
	}
}

func TestSlashLeave_RefusalToasts(t *testing.T) {
	a, _ := leaveApp(t, errors.New("cant_leave_general"))
	a.compose.SetValue("/leave")
	failed := deliveredMsgs[ConversationLeaveFailedMsg](t, pressEnter(a))
	a.Update(failed[0])
	if !toastShows(a, "Could not leave #general: cant_leave_general") {
		t.Errorf("status bar = %q, want the refusal", statusbarText(a))
	}
	if !slices.Contains(sidebarIDs(a), "C1") {
		t.Errorf("sidebar = %v, want C1 kept after a refusal", sidebarIDs(a))
	}
}

func TestConversationClosed_DropsTheSidebarAndFinderRows(t *testing.T) {
	a, _ := leaveApp(t, nil)
	_, cmd := a.Update(ConversationClosedMsg{TeamID: "T1", ChannelID: "C2"})
	if want := []string{"C1", "C3", "D1"}; !slices.Equal(sidebarIDs(a), want) {
		t.Errorf("sidebar = %v, want %v", sidebarIDs(a), want)
	}
	if _, ok := a.findChannelByID("C2"); ok {
		t.Error("the finder still holds C2")
	}
	if a.activeChannelID != "C1" {
		t.Errorf("active channel = %q, want C1 untouched", a.activeChannelID)
	}
	for _, m := range drainBatch(cmd) {
		if sel, ok := m.(ChannelSelectedMsg); ok {
			t.Errorf("selected %+v; closing another conversation must not move the user", sel)
		}
	}
}

func TestConversationClosed_AnotherWorkspaceIsIgnored(t *testing.T) {
	a, _ := leaveApp(t, nil)
	a.Update(ConversationClosedMsg{TeamID: "T2", ChannelID: "C1"})
	if !slices.Contains(sidebarIDs(a), "C1") {
		t.Errorf("sidebar = %v, want C1 kept: the message is for T2", sidebarIDs(a))
	}
}

// Leaving the conversation on screen goes back to the one the user came
// from.
func TestConversationClosed_OnScreenGoesBackInHistory(t *testing.T) {
	a, _ := leaveApp(t, nil)
	a.Update(ChannelSelectedMsg{ID: "C2", Name: "random", Type: "channel"})
	a.Update(ChannelSelectedMsg{ID: "D1", Name: "alice", Type: "dm"})
	if a.activeChannelID != "D1" {
		t.Fatalf("precondition: active channel = %q, want D1", a.activeChannelID)
	}
	_, cmd := a.Update(ConversationClosedMsg{TeamID: "T1", ChannelID: "D1"})
	walkAndUpdate(t, a, cmd)
	if a.activeChannelID != "C2" {
		t.Errorf("active channel = %q, want C2, where the user was before D1", a.activeChannelID)
	}
}

func TestConversationClosed_OnScreenWithoutHistoryOpensTheFirstRow(t *testing.T) {
	a, _ := leaveApp(t, nil)
	typeIn(a, "C2")
	_, cmd := a.Update(ConversationClosedMsg{TeamID: "T1", ChannelID: "C2"})
	sel := deliveredMsgs[ChannelSelectedMsg](t, cmd)
	if len(sel) != 1 || sel[0].ID != "C1" {
		t.Fatalf("ChannelSelectedMsg = %+v, want C1, the first row", sel)
	}
}
