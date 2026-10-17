// Compatibility entrypoint for existing installations and SIAGENT_CHANNEL_WORKER.
import { pathToFileURL } from "node:url";
import { runWorker } from "./plugins/wecom-bot/worker.js";

export { runWorker };

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)
  runWorker();
