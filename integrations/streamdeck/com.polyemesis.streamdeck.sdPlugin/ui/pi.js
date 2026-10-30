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
    const dest = Number($("destinationId").value);
    if (Number.isSafeInteger(dest) && dest > 0) payload.destinationId = dest;
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
    };
    socket.onmessage = onMessage;
    fields.forEach((f) => $(f).addEventListener("change", saveGlobal));
    ["sourceId", "destinationId"].forEach((f) => $(f).addEventListener("change", saveAction));
    $("presetId").addEventListener("change", saveAction);
    $("test").addEventListener("click", runTest);
  };
})();
