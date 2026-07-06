import { spawn } from "node:child_process";

/** Best-effort: opens the user's default browser to the given URL. */
export function openBrowser(url: string): void {
  let command: string;
  let args: string[];
  if (process.platform === "darwin") {
    command = "open";
    args = [url];
  } else if (process.platform === "win32") {
    command = "rundll32";
    args = ["url.dll,FileProtocolHandler", url];
  } else {
    command = "xdg-open";
    args = [url];
  }
  try {
    spawn(command, args, { stdio: "ignore", detached: true }).unref();
  } catch {
    // Best effort — the URL is also printed to the console.
  }
}
