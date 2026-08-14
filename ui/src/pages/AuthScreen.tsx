import { useState } from "react";
import { toast } from "sonner";
import { AudioLines, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
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
  const [busy, setBusy] = useState(false);

  const isSetup = mode === "setup";

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (busy) return;

    if (isSetup) {
      if (password.length < MIN_PASSWORD) {
        toast.error(`Password must be at least ${MIN_PASSWORD} characters.`);
        return;
      }
      if (password !== confirm) {
        toast.error(t("auth.passwordMismatch"));
        return;
      }
    }

    setBusy(true);
    try {
      if (isSetup) {
        await api.setup(username.trim() || "admin", password);
        toast.success(t("auth.accountCreated"));
      } else {
        await api.login(username.trim(), password);
      }
      onDone();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("auth.somethingWentWrong"));
      setPassword("");
      setConfirm("");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex min-h-dvh items-center justify-center bg-surface p-4">
      <div className="w-full max-w-sm">
        <div className="mb-4 flex items-center justify-center gap-2">
          <div className="flex h-7 w-7 items-center justify-center rounded bg-primary/15">
            <AudioLines className="h-4 w-4 text-primary" />
          </div>
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
                              scorePassword(password).score >= seg
                                ? scorePassword(password).score <= 1
                                  ? "bg-down"
                                  : scorePassword(password).score === 2
                                    ? "bg-cross"
                                    : "bg-live"
                                : "bg-line"
                            }`}
                          />
                        ))}
                      </div>
                      <span className="text-[10px] text-muted-foreground" aria-live="polite">
                        At least {MIN_PASSWORD} characters
                        {scorePassword(password).label ? ` · ${scorePassword(password).label}` : ""}
                        {password.length > 0 && password.length < 20 ? " · length helps more than symbols" : ""}
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
                      <p className="mt-2 rounded border border-line bg-raised/40 p-2 text-[10px] leading-relaxed text-muted-foreground">
                        This password protects the admin UI and API. Your stream keys
                        and OAuth tokens are encrypted separately, with a key file in
                        the data directory — back that up alongside the database, or
                        your destinations will not open after a restore.
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
                    required
                  />
                </div>
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
