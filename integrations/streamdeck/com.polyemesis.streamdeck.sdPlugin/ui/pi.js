// Property inspector for every polyemesis action. It talks to the plugin over
// the standard Stream Deck socket and shows only what the plugin sends back.
// The token is saved to GLOBAL settings only, never to action settings.
(function () {
  let socket;
  let actionType;
  let actionContext;
  let pluginUUID;
  let strings = {};
  // The key's saved preset, and the install's list once it arrives. Kept apart because either can
  // arrive first, and the picker must show the SAVED choice, never silently the first preset.
  let savedPresetId = "";
  let presetList = null;
  // The same for a track control's destination and track. The track is a 0-based index kept as a
  // string, so "" (none chosen) is never confused with track 0.
  let savedDestinationId = "";
  let savedTrack = "";
  let destinationList = null;

  // The actions whose inspector picks a destination from a list and one of its tracks.
  const TRACK_ACTIONS = ["com.polyemesis.streamdeck.track-mute"];
  const isTrackAction = () => TRACK_ACTIONS.indexOf(actionType) >= 0;

  const $ = (id) => document.getElementById(id);

  // Stream Deck names Chinese zh_CN; the catalogs use zh-Hans. Anything the
  // inspector has no catalog for falls back to English.
  function localeFrom(sdkLanguage) {
    const name = String(sdkLanguage || "en").replace("_", "-");
    const mapped = name === "zh-CN" ? "zh-Hans" : name;
    const all = window.POLYEMESIS_STRINGS || {};
    return all[mapped] ? mapped : "en";
  }

  function loadStrings(sdkLanguage) {
    const all = window.POLYEMESIS_STRINGS || {};
    strings = Object.assign({}, all.en || {}, all[localeFrom(sdkLanguage)] || {});
    applyStrings();
  }
  const fields = ["url", "token", "caPem"];

  function send(message) {
    if (socket && socket.readyState === 1) socket.send(JSON.stringify(message));
  }

  function applyStrings() {
    document.querySelectorAll("[data-i18n]").forEach((el) => {
      const value = strings[el.getAttribute("data-i18n")];
      if (value !== undefined) el.textContent = value;
    });
  }

  function saveGlobal() {
    send({
      event: "setGlobalSettings",
      context: pluginUUID,
      payload: { url: $("url").value, token: $("token").value, caPem: $("caPem").value },
    });
    flashSaved();
  }

  function flashSaved() {
    const result = $("result");
    result.className = "result ok";
    result.textContent = strings["streamdeck.pi.saved"] || "";
  }

  function saveAction() {
    const payload = {};
    const source = Number($("sourceId").value);
    if (Number.isSafeInteger(source) && source > 0) payload.sourceId = source;
    const dest = Number(isTrackAction() ? savedDestinationId : $("destinationId").value);
    if (Number.isSafeInteger(dest) && dest > 0) payload.destinationId = dest;
    // Number("") is 0, which is a real track: an unchosen track saves nothing.
    const track = savedTrack === "" ? NaN : Number(savedTrack);
    if (isTrackAction() && Number.isSafeInteger(track) && track >= 0) payload.track = track;
    if ($("presetId").value) payload.presetId = $("presetId").value;
    savedPresetId = $("presetId").value;
    send({ event: "setSettings", context: actionContext, payload });
    flashSaved();
  }

  // The preset list comes from the plugin, which reads the SAVED pairing. Never from this page,
  // so a token typed but not yet saved cannot be what lists the presets.
  function requestPresets() {
    send({ action: actionType, event: "sendToPlugin", context: actionContext, payload: { type: "presets" } });
  }

  // The destinations come from the plugin, which reads the SAVED pairing, for the programme in the form.
  function requestDestinations() {
    const source = Number($("sourceId").value);
    const payload = { type: "destinations" };
    if (Number.isSafeInteger(source) && source > 0) payload.sourceId = source;
    send({ action: actionType, event: "sendToPlugin", context: actionContext, payload });
  }

  function showDestinations(payload) {
    const error = $("err-track");
    if (payload.key) {
      error.textContent = strings[payload.key] || strings["streamdeck.pi.destinationsFailed"] || "";
      error.hidden = false;
      renderDestinations();
      return;
    }
    error.hidden = true;
    destinationList = payload.destinations || [];
    renderDestinations();
  }

  function option(value, label) {
    const o = document.createElement("option");
    o.value = value;
    o.textContent = label;
    return o;
  }

  // A picker that shows the SAVED choice whichever arrives first, the settings or the list, and keeps a
  // saved choice the list no longer has visible and marked rather than silently choosing another.
  function fillPicker(select, chooseKey, items, saved) {
    select.replaceChildren(option("", strings[chooseKey] || ""));
    (items || []).forEach((it) => select.appendChild(option(it.value, it.label)));
    if (saved !== "" && items && !items.some((it) => it.value === saved)) {
      select.appendChild(option(saved, (strings["streamdeck.pi.notListed"] || "{id}").replace("{id}", saved)));
    } else if (saved !== "" && !items) {
      select.appendChild(option(saved, saved));
    }
    select.value = saved;
  }

  function renderDestinations() {
    const items = destinationList && destinationList.map((d) => ({ value: String(d.id), label: d.name }));
    fillPicker($("trackDestination"), "streamdeck.pi.destinationChoose", items, savedDestinationId);
    renderTracks();
  }

  function renderTracks() {
    const dest = destinationList && destinationList.find((d) => String(d.id) === savedDestinationId);
    const items = dest ? dest.tracks.map((tr) => ({ value: String(tr.track), label: tr.name })) : destinationList && [];
    fillPicker($("track"), "streamdeck.pi.trackChoose", items, savedTrack);
  }

  function onDestinationChange() {
    savedDestinationId = $("trackDestination").value;
    // A track index means nothing on another destination's selection.
    savedTrack = "";
    renderTracks();
    saveAction();
  }

  function onTrackChange() {
    savedTrack = $("track").value;
    saveAction();
  }

  function onSourceChange() {
    saveAction();
    if (isTrackAction()) requestDestinations();
  }

  function showPresets(payload) {
    const error = $("err-preset");
    if (payload.key) {
      error.textContent = strings[payload.key] || strings["streamdeck.pi.presetsFailed"] || "";
      error.hidden = false;
      renderPresets();
      return;
    }
    error.hidden = true;
    presetList = payload.presets || [];
    renderPresets();
  }

  // An empty "choose" entry first, so nothing is chosen until the operator chooses: a key with no
  // preset is unbound, and the picker says so rather than showing a preset the key does not hold.
  // A saved preset the install no longer lists stays visible, marked, instead of vanishing.
  function renderPresets() {
    const select = $("presetId");
    const option = (value, label) => {
      const o = document.createElement("option");
      o.value = value;
      o.textContent = label;
      return o;
    };
    select.replaceChildren(option("", strings["streamdeck.pi.presetChoose"] || ""));
    const list = presetList || [];
    list.forEach((p) => select.appendChild(option(p.id, p.name)));
    if (savedPresetId && presetList && !list.some((p) => p.id === savedPresetId)) {
      select.appendChild(option(savedPresetId, (strings["streamdeck.pi.presetMissing"] || "{id}").replace("{id}", savedPresetId)));
    } else if (savedPresetId && !presetList) {
      select.appendChild(option(savedPresetId, savedPresetId));
    }
    select.value = savedPresetId;
  }

  function showGlobal(settings) {
    fields.forEach((f) => {
      if (typeof settings[f] === "string") $(f).value = settings[f];
    });
  }

  function showAction(settings) {
    if (typeof settings.sourceId === "number") $("sourceId").value = String(settings.sourceId);
    if (typeof settings.destinationId === "number") $("destinationId").value = String(settings.destinationId);
    savedPresetId = typeof settings.presetId === "string" ? settings.presetId : "";
    renderPresets();
    if (isTrackAction()) {
      savedDestinationId = typeof settings.destinationId === "number" ? String(settings.destinationId) : "";
      savedTrack = typeof settings.track === "number" ? String(settings.track) : "";
      renderDestinations();
    }
  }

  function runTest() {
    const url = $("url").value.trim();
    const token = $("token").value.trim();
    const result = $("result");
    result.className = "result";
    result.textContent = strings["streamdeck.pi.testing"] || "";
    $("admin-warning").hidden = true;
    $("test").disabled = true;
    send({
      action: actionType,
      event: "sendToPlugin",
      context: actionContext,
      payload: { type: "test", url, token, caPem: $("caPem").value },
    });
  }

  function onTestResult(payload) {
    $("test").disabled = false;
    const result = $("result");
    if (payload.ok) {
      result.className = "result ok";
      result.textContent = strings["streamdeck.auth.connected"] || "";
      if (payload.adminScope && strings["streamdeck.auth.adminWarning"]) {
        $("admin-warning").textContent = strings["streamdeck.auth.adminWarning"];
        $("admin-warning").hidden = false;
      }
      return;
    }
    result.className = "result bad";
    const label = strings[payload.key] || "";
    result.textContent = payload.detail ? `${label}: ${payload.detail}` : label;
  }

  function onMessage(raw) {
    const msg = JSON.parse(raw.data);
    if (msg.event === "didReceiveGlobalSettings") showGlobal(msg.payload.settings || {});
    else if (msg.event === "didReceiveSettings") showAction(msg.payload.settings || {});
    else if (msg.event === "sendToPropertyInspector") {
      const payload = msg.payload || {};
      if (payload.type === "test") onTestResult(payload);
      else if (payload.type === "presets") showPresets(payload);
      else if (payload.type === "destinations") showDestinations(payload);
    }
  }

  window.connectElgatoStreamDeckSocket = function (port, uuid, registerEvent, info, actionInfo) {
    const action = JSON.parse(actionInfo);
    const registration = typeof info === "string" ? JSON.parse(info) : info || {};
    loadStrings(registration.application && registration.application.language);
    pluginUUID = uuid;
    actionType = action.action;
    actionContext = action.context;
    $("block-destination").hidden = actionType !== "com.polyemesis.streamdeck.destination" && actionType !== "com.polyemesis.streamdeck.stop-destination";
    $("block-track").hidden = !isTrackAction();
    $("block-preset").hidden = actionType !== "com.polyemesis.streamdeck.preset";
    $("block-install-wide").hidden = actionType !== "com.polyemesis.streamdeck.record";
    // Recording is install-wide: the Record key takes no programme, so it shows no programme field.
    $("block-source").hidden = actionType === "com.polyemesis.streamdeck.record";
    socket = new WebSocket("ws://127.0.0.1:" + port);
    socket.onopen = function () {
      send({ event: registerEvent, uuid: uuid });
      send({ event: "getGlobalSettings", context: uuid });
      send({ event: "getSettings", context: actionContext });
      if (!$("block-preset").hidden) requestPresets();
      if (isTrackAction()) requestDestinations();
    };
    socket.onmessage = onMessage;
    fields.forEach((f) => $(f).addEventListener("change", saveGlobal));
    $("sourceId").addEventListener("change", onSourceChange);
    $("destinationId").addEventListener("change", saveAction);
    $("trackDestination").addEventListener("change", onDestinationChange);
    $("track").addEventListener("change", onTrackChange);
    $("presetId").addEventListener("change", saveAction);
    $("test").addEventListener("click", runTest);
  };
})();
