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
      toast.error(err instanceof Error ? err.message : "Something went wrong.");
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
                  <span className="text-[10px] text-muted-foreground">
                    At least {MIN_PASSWORD} characters.
                  </span>
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
