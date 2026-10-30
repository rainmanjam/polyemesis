# polyemesis for Streamer.bot

Streamer.bot actions that control a polyemesis install over its REST API:

- go live
- stop all
- start or stop one destination
- mute or unmute an audio track, live
- recording on or off
- a destination status line for chat

Polyemesis needs no setup for this and stores nothing for it.

| File | What it is |
|---|---|
| `Polyemesis.cs` | The C# that makes every call. One file, one Execute C# Code sub-action. |
| `actions.json` | Every action: its method, the REST call it makes, and the token scope it needs. |

## Setup

1. **Token.** In polyemesis, **Settings → API tokens**, create a token.
   - The controls need the **admin** scope.
   - A token for **Status** alone can be **read**.
2. **Globals.** In Streamer.bot, **Variables → Global Variables**, add these
   as **persisted** globals:
   - `polyemesisUrl`: your install, such as `https://polyemesis.example:8080`
   - `polyemesisToken`: the token
   - `polyemesisSource`: optional. A programme id, if your install runs more
     than one.

   The token goes in a global rather than in the code because an exported
   action carries its code. A token typed into the code would travel with
   every action you share.
3. **The library action.** Create an action named `polyemesis: library`. Add
   **Core → C# → Execute C# Code**, paste all of `Polyemesis.cs`, then press
   **Find Refs** and **Compile**. It references `System.Net.Http.dll` and
   Newtonsoft.Json, which Streamer.bot ships.
4. **One action per control.** For each row of `actions.json`:
   - Create an action with that name.
   - Add **Core → C# → Execute C# Method**, choose the `polyemesis: library`
     action, and type the method name. For example, `StartAll`.
   - For the destination and track actions, first add **Core → Arguments → Set
     Argument** for `destinationId` and `track`. Find the ids on
     `GET /api/v1/destinations`. A track is 0-based, so OBS's track 1 is `0`.
5. Wire the actions to whatever triggers you like: a chat command, a hotkey,
   or a Stream Deck button through Streamer.bot.

Streamer.bot's import strings are not documented, so this pack is source plus
setup steps, not an import string.

**Stop all ends every live YouTube broadcast on the programme, for good.**
Polyemesis refuses it without `{"confirm": true}`, and this action sends that.
So give it a trigger nobody can fire by accident. A chat command needs a
permission check in front of it.

## Self-signed installs

Streamer.bot uses Windows' certificate store. Either give polyemesis a trusted
certificate (see docs/TLS.md), or import the install's CA from
`GET /api/v1/tls/ca` into **Trusted Root Certification Authorities** for the
user that runs Streamer.bot. The C# never turns verification off.

## Mix It Up

Mix It Up can make the same calls: each row of `actions.json` gives the
method, the path and the body. Send `Authorization: Bearer <token>` and
`Content-Type: application/json`. No importable Mix It Up file is shipped, and
none of this has been tried in Mix It Up.

## Testing

`internal/api/streamerbot_pack_test.go` replays every request in
`actions.json` against the real polyemesis router. Each request uses a token of
the scope the manifest declares, and an admin action is also tried with a read
token, which must be refused. The test also checks that `Polyemesis.cs` calls
no path the manifest does not list. Streamer.bot itself is not run.
