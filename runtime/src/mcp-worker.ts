import { spawn, type ChildProcess } from "node:child_process";

// A separate Node guardian keeps an IPC lifeline to Runtime. If Runtime is
// SIGKILLed, disconnect still lets this process kill its MCP process group.
// The guardian never loads server code into its own VM.
let child: ChildProcess | undefined;
function killGroup() {
  if (process.platform !== "win32") {
    try {
      process.kill(-process.pid, "SIGKILL");
    } catch {
      /* Already closed. */
    }
  } else child?.kill("SIGKILL");
  process.exit(1);
}
process.on("disconnect", killGroup);
process.on("SIGTERM", () => {
  child?.kill("SIGTERM");
  setTimeout(killGroup, 1000);
});
process.once(
  "message",
  (message: {
    command: string;
    args: string[];
    env: Record<string, string>;
  }) => {
    child = spawn(message.command, message.args, {
      env: message.env,
      shell: false,
      stdio: ["pipe", "pipe", "ignore"],
    });
    child.on("error", killGroup);
    child.on("close", killGroup);
    child.stdin!.on("error", () => {});
    process.stdout.on("error", killGroup);
    process.stdin.pipe(child.stdin!);
    child.stdout!.pipe(process.stdout);
  },
);
