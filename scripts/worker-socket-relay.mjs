// Dev only: relays a host TCP port to the worker admin socket inside a container.
import { spawn } from "node:child_process";
import { createServer } from "node:net";

const [container, socket = "/run/openneko/worker.sock"] = process.argv.slice(2);
if (!container) {
  console.error("usage: node scripts/worker-socket-relay.mjs <container> [socket]");
  process.exit(2);
}
const inner = `const s=require("net").connect(${JSON.stringify(socket)});process.stdin.pipe(s);s.pipe(process.stdout);s.on("close",()=>process.exit(0));s.on("error",()=>process.exit(1))`;
const server = createServer((client) => {
  const child = spawn("docker", ["exec", "-i", container, "node", "-e", inner], { stdio: ["pipe", "pipe", "ignore"] });
  client.pipe(child.stdin);
  child.stdout.pipe(client);
  client.on("close", () => child.kill());
  client.on("error", () => child.kill());
  child.on("exit", () => client.destroy());
});
server.listen(0, "127.0.0.1", () => console.log(server.address().port));
