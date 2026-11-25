# Leave a conversation - design

Date: 2026-11-24
Status: implemented - /leave, /close, /part, and the closed-conversation
events.

## Problem

slk could join a channel and open a DM, but it could not undo either.
`/leave`, `/close` and `/part` answered "isn't supported in slk yet"
(slash commands design, section 3.3), and a conversation left in another
Slack client kept its sidebar row until slk restarted. A user who lives
in slk had to open another client to tidy the sidebar.

## What it does

`/leave` takes the conversation the composer belongs to out of the
user's list. `/close` and `/part` are aliases, as `client.appCommands`
lists them. A thread composer leaves the thread's channel.

- A public or private channel is left with `conversations.leave`.
- A DM, group DM or app DM is closed with `conversations.close`. The
  conversation and its history stay on Slack. It comes back when someone
  posts in it.
- A private channel asks first ("You can't rejoin a private channel
  unless someone invites you."), because the user cannot undo it alone.
  Public channels and DMs do not ask: `/open` and `/msg` bring them back.
- The command takes no argument, as in Slack (`usage` is empty in
  `client.appCommands`). `/leave #other` is refused with the usage line
  and the typed text is given back. It never leaves the current channel
  when the user named another one.

Both calls are the documented Web API methods. There is no capture of
the web client's own `/leave` request, unlike the built-ins in the slash
commands design.

## Flow

The sidebar row is not removed optimistically. One path removes it,
whoever made the change:

1. `ChannelService.Leave(teamID, channelID, channelType)` makes the call
   for the workspace the user was looking at.
2. A refusal comes back as `ConversationLeaveFailedMsg` and toasts
   "Could not leave #general: cant_leave_general". Nothing was removed.
3. On success the command hands the channel ID to the WebSocket event
   owner (`queueConversationClosed`, on the pending-events wake the star
   queue already uses). Only the owner may write `wctx.Channels`.
4. The owner runs `removeConversation`: both workspace snapshots, the
   conversation set, and the cache row's `is_member` flag. For the
   active workspace it sends `ConversationClosedMsg`.
5. The App drops the sidebar and finder rows. If the conversation was
   the one on screen it goes back one step in the navigation history,
   or opens the first sidebar row when there is no history.

Slack sends `channel_left`, `group_left`, `im_close` and `mpim_close` to
every client of the user. The dispatcher maps all four to
`OnConversationClosed`, which runs the same `removeConversation`. So
a leave made in another client removes the row live, and the echo of a
leave made here finds no row and sends nothing. The queue in step 3
means the removal does not depend on that echo arriving.

## Decisions

No optimistic removal. `/star` moves its row before Slack answers and
has to keep a snapshot to move it back. A leave is rare and Slack does
refuse some (`cant_leave_general`, the last member of a private
channel), so waiting for the answer costs one round trip and needs no
undo state.

Back in history, not the default channel. A workspace switch that finds
its last channel gone opens `default_channel`. After a leave the user
was somewhere a moment ago, and the history walk already skips
conversations that are gone, so it returns them there.

A left channel is not rediscovered. `discoverConversation` adds a row
for a message on a conversation the notifier maps do not hold. Slack
can post a "left the channel" notice in the channel being left, and that
message must not bring the row back. A left channel keeps its notifier
entry for that reason. A closed DM loses it, so a later message reopens
the DM, as in Slack. A `channel_joined` for the same channel replaces
the entry as before.

New slices on removal. A workspace switch hands the UI the backing
arrays of `wctx.Channels` and `wctx.FinderItems`. `removeConversation`
builds new slices so it never shifts rows under a reader.

## Limits

- A split window that is not focused keeps showing a conversation that
  was left. Its messages stay readable. Sending there fails with Slack's
  own error.
- A leave that arrives while an upload is in flight removes the row but
  does not move the user: a channel switch is refused during an upload.
- Not tested against Enterprise Grid. A member workspace may answer
  `team_is_restricted`, which shows as the toast.
- A starred conversation can come back at the next reconnect. slk
  recovers starred conversations it holds no row for, a closed starred
  DM among them, so the row returns unless Slack also removed the star.
- The four event shapes follow Slack's RTM documentation. They are not
  pinned to a WebSocket capture. `group_close` is left out on purpose:
  it predates `mpim_close`, and removing a private channel the user is
  still in would hide it until the next start.
