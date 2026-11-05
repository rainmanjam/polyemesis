import { it } from "vitest";
import { PolyemesisClient, type Transport } from "../../src/api.js";

// NOT PART OF THE SUITE. test/contract.test.ts runs this file in a child vitest and expects it to FAIL: it
// serves the client a fake whose field the server does not send, and leaves the violation for test/setup.ts's
// afterEach to find. If that hook is ever disabled, this passes and contract.test.ts fails.

it("serves a renamed field and does not take the violation", async () => {
  const transport: Transport = {
    send() {
      return Promise.resolve({ status: 200, body: JSON.stringify({ destinations: [{ id: 1, name: "x", reason: { cause: "dns", isHeld: true } }] }) });
    },
  };
  await new PolyemesisClient(new URL("https://studio.example"), "t", transport).status(1);
});
