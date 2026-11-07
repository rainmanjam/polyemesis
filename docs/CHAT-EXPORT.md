# Chat export and chat saved with recordings

Chat history is a replay buffer, not an archive: the chat pane and the moderator
cards read it, and **Settings → Pipeline → Chat history** decides how long it
is kept (two hours or the newest 2000 messages by default). For analysing a
stream afterwards that is not enough, so polyemesis saves the chat of every
recording beside the recording, and can export any window that is still stored.

## Saved with each recording

When a recording file finishes, two files appear next to it in the recordings
directory:

| File | What it is |
|---|---|
| `<recording>.chat.json` | every message that arrived during the recording, with a header |
| `<recording>.chat.vtt` | the same as WebVTT cues, `[PLATFORM] name: text`, to replay chat against the video in a player or editor |

They are plain files, not database rows, so they survive the chat purge and the
database. They follow their recording: deleting a recording (the button,
the age or size sweep) deletes them, a sweep removes any whose recording is
gone, and [offload](STORAGE.md) copies them with it. The Recordings page shows a
**Chat** menu on every row, and `GET /recordings` reports `chatSaved`.

A file is written once the recording file has not been touched for two minutes,
so the last file of a stream is saved too. A window with no chat gets no file
(an empty file would read as "nobody chatted" when the purge may simply have got
there first).

### Timing

Every offset and absolute time is the **arrival time**: when this server
received the message, not the platform's own timestamp. It is the same clock the
highlight scanner uses to line chat up with a recording. Platform clocks drift,
differ from each other and say nothing about where a message sits against
footage this server recorded.

The recording's start is the timestamp in its file name, **whole seconds**, so
offsets are good to about a second against the video (`startPrecision` says
so). The window ends at the file's measured duration, or at its last write for a
file that was never measured (the last one of a stream). Consecutive files of one
programme are closed up when their computed windows disagree by under five
seconds, so each message lands in exactly one file of a session rather than none
or two.

### What is in and out

- **In:** viewer messages, with their author's display name and id, platform,
  account, channel, text, roles (`broadcaster`, `moderator`, `subscriber`),
  badges and reply target.
- **Out:** messages polyemesis sent itself (echo), and messages that were
  deleted by a moderator or hidden in polyemesis before the file was written.
  Both remove the stored row, so they were never candidates. (A ban or a room
  clear removes only the author's messages the live ring still holds, so older
  messages from a banned person can remain in the history and in a file.) A message hidden
  locally is still visible to the platform's audience, which means an analysis
  here under-counts it. A deletion that happens *after* the file was written is
  not applied to it.
- Emotes and author colours are not exported.

### The JSON file

```json
{"schema":1,"generator":"polyemesis v1.2.3","generatedAt":"2026-03-04T21:12:09Z",
"recording":{"file":"rec-s1-20260304-200000.mkv","startedAt":"2026-03-04T20:00:00Z","durationMs":3600000,"endSource":"probe","startPrecision":"1s"},
"from":"2026-03-04T20:00:00Z","to":"2026-03-04T21:00:00Z","platforms":["twitch","youtube"],"messageCount":2,
"clock":"arrival: the time this server received each message, not the platform's own timestamp",
"excludes":["echo","moderation-removed"],"complete":true,"messages":[
{"offsetMs":2500,"at":"2026-03-04T20:00:02.5Z","atMs":1772654402500,"platform":"twitch","account":"acct","messageId":"m1","authorId":"u1","author":"Gull Watcher","text":"seven gulls <3","roles":["subscriber"],"badges":[{"id":"subscriber","version":"12"}]},
{"offsetMs":61000,"at":"2026-03-04T20:01:01Z","atMs":1772654461000,"platform":"youtube","account":"chan","messageId":"m2","authorId":"u2","author":"Sam","text":"first line","roles":[],"badges":[]}
]}
```

`complete` says whether the chat history still reached back to the start of the
window when the file was written. When it is `false` the file holds only the
tail and `retainedFrom` says from when. `schema` changes only when a field changes meaning.

### The VTT file

```
WEBVTT

NOTE
polyemesis chat for rec-s1-20260304-200000.mkv
started 2026-03-04T20:00:00Z

00:00:02.500 --> 00:00:07.500
[TWITCH] Gull Watcher: seven gulls &lt;3

00:01:01.000 --> 00:01:06.000
[YOUTUBE] Sam: first line
```

Each cue is on screen for five seconds and cues may overlap, so a burst stacks.
Text is escaped and line breaks become spaces.

## Edge cases

- **A recording that spans a purge.** The file is written when the recording
  finishes, from whatever the chat history still holds, and says so
  (`complete: false`, `retainedFrom`). Keep the history longer than one segment
  (3600 seconds by default) plus a few minutes and every file is complete; the
  default two hours is.
- **Two programmes recording at once.** `chat_messages` records no programme, so
  each file holds *all* the chat that arrived during its window, the other
  programme's included. The highlight scanner has the same limit.
- **A session recorded as several files.** Each file has its own sidecar and
  offsets from its own start. Overlapping clocks put a message in both
  files, each with its own offset.
- **Deleted or hidden messages.** See above.
- **Very long recordings.** Nothing is held in memory: messages are read a page
  at a time and written as they come, so a day-long file costs a few kilobytes
  of memory and some disk. The writer holds the database for one page at a time
  only, never for a whole export.
- **No sidecar.** An older recording, or one whose chat was purged before the
  file was written, can still be exported while the history holds its window.

## On-demand export

- `GET /recordings/{id}/chat?format=json|vtt` serves the saved file, or builds
  the same file from the history if there is none, or answers `404` saying the
  chat is no longer stored.
- `GET /chat/export?from=…&to=…&format=json|csv|ndjson&platform=…` streams any
  window. `from` and `to` are RFC 3339 times or Unix milliseconds; `to` defaults
  to now. The Chat page has an **Export chat** button for it.

CSV cells that start with `=`, `+`, `-` or `@` get a leading `'` so a
spreadsheet does not run them as formulas. Both routes carry viewer names and
are refused to `read` tokens; see [API.md](API.md#platforms-metadata-chat).
