// polyemesis for Streamer.bot.
//
// Paste this whole file into ONE "Execute C# Code" sub-action, compile it, and
// save it as an action named "polyemesis: library". Every other action in the
// pack is then an "Execute C# Method" sub-action that calls one method below
// on that action (see README.md). actions.json lists every method and the one
// REST call it makes.
//
// CONFIGURATION, in Streamer.bot global variables, PERSISTED:
//   polyemesisUrl     https://host:8080   (no trailing slash needed); http://
//                     is accepted only for this machine (localhost, 127.0.0.1)
//   polyemesisToken   an API token: "admin" scope for the controls, "read"
//                     is enough for Status
//   polyemesisSource  optional programme id, for an install with more than one
//
// THE TOKEN LIVES IN A GLOBAL, NOT IN THIS CODE. An action exported from
// Streamer.bot carries its code; a token typed into it would travel with every
// export you share. Globals are not exported with actions. This file never
// logs the token, and sends it only to polyemesisUrl, and only over https:// (or
// http:// to this machine): the same rule as the Stream Deck plugin and the OBS
// script.
//
// Tested against the polyemesis REST API by internal/api/streamerbot_pack_test.go,
// which replays every request in actions.json. Not tested inside Streamer.bot:
// nothing in the polyemesis repository runs Streamer.bot.

using System;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text;
using Newtonsoft.Json.Linq;

public class CPHInline
{
    private static readonly HttpClient Http = new HttpClient { Timeout = TimeSpan.FromSeconds(30) };

    public bool Execute()
    {
        CPH.LogInfo("polyemesis: call one of this action's methods with Execute C# Method");
        return true;
    }

    // ---- the actions -----------------------------------------------------

    public bool StartAll()
    {
        return Send("POST", "/api/v1/destinations/start-all" + SourceQuery(), null);
    }

    // Stop all ENDS EVERY LIVE YOUTUBE BROADCAST ON THE PROGRAMME, permanently.
    // polyemesis refuses it without {"confirm": true}; this sends that, so put
    // this action behind something you cannot press by accident.
    public bool StopAll()
    {
        return Send("POST", "/api/v1/destinations/stop-all" + SourceQuery(), "{\"confirm\":true}");
    }

    public bool StartDestination()
    {
        string id = Arg("destinationId");
        return id != null && Send("POST", "/api/v1/destinations/" + id + "/start", null);
    }

    public bool StopDestination()
    {
        string id = Arg("destinationId");
        return id != null && Send("POST", "/api/v1/destinations/" + id + "/stop", null);
    }

    public bool MuteTrack() { return TrackMute(true); }

    public bool UnmuteTrack() { return TrackMute(false); }

    public bool RecordingOn() { return Send("POST", "/api/v1/recording", "{\"enabled\":true}"); }

    public bool RecordingOff() { return Send("POST", "/api/v1/recording", "{\"enabled\":false}"); }

    // Status sets %polyemesisLive% and %polyemesisSummary% for a following
    // Send Message sub-action.
    public bool Status()
    {
        string body;
        if (!Request("GET", "/api/v1/destinations" + SourceQuery(), null, out body)) return false;
        int live = 0, total = 0;
        foreach (JToken d in JArray.Parse(body))
        {
            total++;
            if (d.Value<bool?>("enabled") == true) live++;
        }
        CPH.SetArgument("polyemesisLive", live);
        CPH.SetArgument("polyemesisSummary", live + " of " + total + " destinations running");
        return true;
    }

    // ---- plumbing --------------------------------------------------------

    private bool TrackMute(bool muted)
    {
        string id = Arg("destinationId"), track = Arg("track");
        if (id == null || track == null) return false;
        return Send("POST", "/api/v1/destinations/" + id + "/tracks/" + track + "/gain",
            muted ? "{\"muted\":true}" : "{\"muted\":false}");
    }

    // Arg reads an action argument that must be a whole number, so nothing a
    // chat command typed can reshape the path.
    private string Arg(string name)
    {
        object v;
        if (!args.TryGetValue(name, out v) || v == null)
        {
            CPH.LogWarn("polyemesis: set the " + name + " argument on this action");
            return null;
        }
        long n;
        if (!long.TryParse(v.ToString(), out n) || n < 0)
        {
            CPH.LogWarn("polyemesis: " + name + " must be a whole number");
            return null;
        }
        return n.ToString();
    }

    private string SourceQuery()
    {
        string src = CPH.GetGlobalVar<string>("polyemesisSource", true);
        long n;
        return (!string.IsNullOrWhiteSpace(src) && long.TryParse(src, out n)) ? "?source=" + n : "";
    }

    private bool Send(string method, string path, string json)
    {
        string ignored;
        return Request(method, path, json, out ignored);
    }

    // The hosts plain http:// may be used with: this machine only.
    private static readonly string[] Loopback = { "localhost", "127.0.0.1", "[::1]" }; // Uri.Host spells IPv6 with brackets

    // Refusal returns null when the token may be sent to url, else the reason.
    // THE TOKEN GOES ONLY WHERE IT CANNOT BE READ ON THE WAY. Mirrors
    // integrations/streamdeck/src/config.ts parseServerUrl and
    // integrations/obs/polyemesis_obs.py parse_server: https:// to anywhere,
    // http:// only to this machine.
    private static string Refusal(string url)
    {
        Uri u;
        if (!Uri.TryCreate(url, UriKind.Absolute, out u))
            return "the address is not a URL; use https://host:port";
        if (u.Host == "")
            return "the address has no host; use https://host:port";
        if (u.Scheme == "https") return null;
        if (u.Scheme == "http" && Array.IndexOf(Loopback, u.Host) >= 0) return null;
        return "refusing " + u.Scheme + "://" + u.Host + ": the API token would cross the network unencrypted. "
            + "Use https://, or http:// only to this machine (localhost)";
    }

    private bool Request(string method, string path, string json, out string body)
    {
        body = null;
        string url = (CPH.GetGlobalVar<string>("polyemesisUrl", true) ?? "").Trim().TrimEnd('/');
        string token = (CPH.GetGlobalVar<string>("polyemesisToken", true) ?? "").Trim();
        if (url == "" || token == "")
        {
            CPH.LogWarn("polyemesis: set the polyemesisUrl and polyemesisToken global variables (persisted)");
            return false;
        }
        string refusal = Refusal(url);
        if (refusal != null)
        {
            CPH.LogWarn("polyemesis: " + refusal);
            return false;
        }
        var req = new HttpRequestMessage(new HttpMethod(method), url + path);
        req.Headers.Authorization = new AuthenticationHeaderValue("Bearer", token);
        if (json != null) req.Content = new StringContent(json, Encoding.UTF8, "application/json");
        try
        {
            HttpResponseMessage resp = Http.SendAsync(req).GetAwaiter().GetResult();
            body = resp.Content.ReadAsStringAsync().GetAwaiter().GetResult();
            if (!resp.IsSuccessStatusCode)
            {
                // The status and polyemesis's own sentence; never the token.
                CPH.LogWarn("polyemesis: " + method + " " + path + " answered " + (int)resp.StatusCode + " " + body);
                return false;
            }
            return true;
        }
        catch (Exception e)
        {
            CPH.LogWarn("polyemesis: " + method + " " + path + " failed: " + e.Message);
            return false;
        }
    }
}
