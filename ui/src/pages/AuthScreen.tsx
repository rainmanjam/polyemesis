import { useState } from "react";
import { toast } from "sonner";
import { Loader2 } from "lucide-react";
import { BrandMark } from "@/components/BrandMark";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, ApiError } from "@/lib/api";
import { useT } from "@/lib/i18n";

const MIN_PASSWORD = 8;

/** First-run setup and sign-in share a screen: they differ only in what the
 *  submit does and whether the password is confirmed. */
/* Password strength, scored on what actually resists an offline attack.
 *
 * LENGTH DOMINATES, and the weighting says so: every character class you add
 * multiplies the search space once, while every character multiplies it again.
 * A 20-character passphrase of nothing but lowercase beats an 8-character one
 * with a symbol in it, and a meter that rewards "P@ssw0rd!" over "correct horse
 * battery staple" teaches the wrong lesson to the one person whose password
 * protects the entire admin surface.
 *
 * Deliberately NOT zxcvbn: it is excellent and it is ~400 KB, more than this UI
 * spends on any other single concern. This is a hint. The server enforces the
 * minimum, and the server is what decides.
 */
const PW_CLASSES = [/[a-z]/, /[A-Z]/, /[0-9]/, /[^A-Za-z0-9]/];

function scorePassword(pw: string): { score: number; label: string } {
  if (!pw) return { score: 0, label: "" };
  const classes = PW_CLASSES.filter((r) => r.test(pw)).length;
  // A repeated or sequential run adds length without adding search space.
  const weak = /^(.)\1+$/.test(pw) || /^(012|123|234|345|456|567|678|789|abc|qwe)/i.test(pw);
  let score = 0;
  if (pw.length >= 12) score += 2;
  else if (pw.length >= 8) score += 1;
  if (pw.length >= 20) score += 1;
  if (classes >= 3) score += 1;
  if (weak) score = Math.min(score, 1);
  score = Math.max(0, Math.min(4, score));
  return { score, label: ["", "weak", "fair", "good", "strong"][score] };
}

/** The strength word for each score, translated at render. Index 0 is never
 *  shown: an empty password has no label. */
const STRENGTH_KEYS = [
  "i18nSweep.strengthWeak",
  "i18nSweep.strengthWeak",
  "i18nSweep.strengthFair",
  "i18nSweep.strengthGood",
  "i18nSweep.strengthStrong",
] as const;

export function AuthScreen({
  mode,
  onDone,
}: {
  mode: "setup" | "login";
  onDone: () => void;
}) {
  const t = useT();
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [setupCode, setSetupCode] = useState("");
  const [busy, setBusy] = useState(false);
  // The one error on the form, shown inline where the operator is looking. A
  // validation problem used to be a toast that vanished before it was read (A75).
  const [error, setError] = useState("");

  const isSetup = mode === "setup";
  // Computed once per render: the four segments and the line below all read it.
  const strength = scorePassword(password);
  // Shown under Confirm while the operator types, not after they press the button.
  const mismatch = isSetup && confirm.length > 0 && password !== confirm;

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (busy) return;

    if (isSetup) {
      if (password.length < MIN_PASSWORD) {
        setError(t("i18nSweep.passwordTooShort", { count: MIN_PASSWORD }));
        return;
      }
      if (password !== confirm) {
        setError(t("auth.passwordMismatch"));
        return;
      }
    }

    setBusy(true);
    setError("");
    try {
      if (isSetup) {
        await api.setup(username.trim() || "admin", password, setupCode.trim());
        toast.success(t("auth.accountCreated"));
      } else {
        await api.login(username.trim(), password);
      }
      onDone();
    } catch (err) {
      setError(err instanceof Error ? err.message : t("auth.somethingWentWrong"));
      // Only a rejected password is cleared, because it must be retyped. A wrong
      // setup code or a dropped connection keeps what was typed: the password is
      // not what went wrong, and retyping it is the cost of a typo elsewhere (A75).
      if (err instanceof ApiError && err.status === 401) {
        setPassword("");
        setConfirm("");
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex min-h-dvh items-center justify-center bg-surface p-4">
      <div className="w-full max-w-sm">
        <div className="mb-4 flex items-center justify-center gap-2">
          <BrandMark size={26} />
          <span className="text-lg font-semibold tracking-tight">polyemesis</span>
        </div>

        <Card>
          <CardHeader>
            <CardTitle>{isSetup ? t("auth.firstRun") : t("auth.signIn")}</CardTitle>
            <CardDescription>
              {isSetup
                ? t("auth.firstRunDesc") : t("auth.signInDesc")}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <form onSubmit={submit} className="flex flex-col gap-3">
              {/* FIRST, AND REQUIRED. Whoever reached a fresh install's port
                  first used to become its admin; the code proves the person
                  at this form can also read the box. It is not a secret to
                  hide from its owner -- a plain text input, so it can be
                  checked against the terminal it was copied from. */}
              {isSetup && (
                <div className="flex flex-col gap-1">
                  <Label htmlFor="setupCode">{t("auth.setupCode")}</Label>
                  <Input
                    id="setupCode"
                    value={setupCode}
                    autoComplete="off"
                    spellCheck={false}
                    autoCapitalize="characters"
                    placeholder="XXXX-XXXX-XXXX-XXXX"
                    aria-describedby="setupCode-hint"
                    onChange={(e) => setSetupCode(e.target.value)}
                    required
                    autoFocus
                  />
                  {/* The routes to the code, one per line, behind a disclosure. The
                      four commands used to share one dense paragraph with no word
                      on which applies (A76). */}
                  <details id="setupCode-hint" className="text-sm text-muted-foreground">
                    <summary className="cursor-pointer hover:text-foreground">
                      {t("auth.setupCodeWhere")}
                    </summary>
                    <div className="mt-1 flex flex-col gap-1 leading-relaxed">
                      <p>{t("auth.setupCodeLinux")}</p>
                      <p>{t("auth.setupCodeDocker")}</p>
                      <p>{t("auth.setupCodeOther")}</p>
                    </div>
                  </details>
                </div>
              )}
              <div className="flex flex-col gap-1">
                <Label htmlFor="username">{t("auth.username")}</Label>
                <Input
                  id="username"
                  value={username}
                  autoComplete="username"
                  onChange={(e) => setUsername(e.target.value)}
                  required
                />
              </div>

              <div className="flex flex-col gap-1">
                <Label htmlFor="password">{t("auth.password")}</Label>
                <Input
                  id="password"
                  type="password"
                  value={password}
                  autoComplete={isSetup ? "new-password" : "current-password"}
                  onChange={(e) => setPassword(e.target.value)}
                  required
                  autoFocus={!isSetup}
                />
                {isSetup && (
                    <>
                      {/* Four segments rather than a percentage bar: a bar invites
                          you to fill it, and there is no score at which a password
                          is finished. */}
                      <div className="mt-1 flex gap-1" aria-hidden="true">
                        {[1, 2, 3, 4].map((seg) => (
                          <div
                            key={seg}
                            className={`h-1 flex-1 rounded-full ${
                              strength.score >= seg
                                ? strength.score <= 1
                                  ? "bg-down"
                                  : strength.score === 2
                                    ? "bg-warn"
                                    : "bg-live"
                                : "bg-border"
                            }`}
                          />
                        ))}
                      </div>
                      <p className="text-sm text-muted-foreground">
                        {t("i18nSweep.passwordMin", { count: MIN_PASSWORD })}
                        {strength.label ? ` · ${t(STRENGTH_KEYS[strength.score])}` : ""}
                        {password.length > 0 && password.length < 20 ? ` · ${t("i18nSweep.passwordLengthHelps")}` : ""}
                      </p>
                      {/* The live region holds ONLY the strength word, so a screen
                          reader announces a change of strength, not every keystroke
                          (A77). It is visually hidden; the sentence above is the
                          visible copy. */}
                      <span className="sr-only" aria-live="polite">
                        {strength.label ? t(STRENGTH_KEYS[strength.score]) : ""}
                      </span>
                      {/* WHAT THIS PASSWORD DOES, AND WHAT IT DOES NOT.
                          It protects the UI and the API. It encrypts nothing:
                          internal/secrets generates a separate key file on first
                          run and deliberately does not derive it from this
                          password, because the server must refresh OAuth tokens
                          while nobody is logged in.

                          This is the only place an operator learns that restoring
                          a database WITHOUT that key file leaves every destination
                          disabled -- which is by design, and is otherwise found
                          out during a restore, which is the worst moment. */}
                      <p className="mt-2 rounded border border-border bg-card-raised/40 p-2 text-sm leading-relaxed text-muted-foreground">
                        {t("i18nSweep.passwordProtects")}
                      </p>
                    </>
                  )}
              </div>

              {isSetup && (
                <div className="flex flex-col gap-1">
                  <Label htmlFor="confirm">{t("auth.confirmPassword")}</Label>
                  <Input
                    id="confirm"
                    type="password"
                    value={confirm}
                    autoComplete="new-password"
                    onChange={(e) => setConfirm(e.target.value)}
                    aria-invalid={mismatch || undefined}
                    aria-describedby={mismatch ? "confirm-error" : undefined}
                    required
                  />
                  {mismatch && (
                    <p id="confirm-error" className="text-sm text-down">
                      {t("auth.passwordMismatch")}
                    </p>
                  )}
                </div>
              )}

              {error && (
                <p role="alert" className="text-sm text-down">
                  {error}
                </p>
              )}

              <Button type="submit" disabled={busy} className="mt-1">
                {busy && <Loader2 className="animate-spin" />}
                {isSetup ? t("auth.createAccount") : t("auth.signIn")}
              </Button>
            </form>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
