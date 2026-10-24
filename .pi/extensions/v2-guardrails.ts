/**
 * v2 guardrails in the agent loop (docs/iteration-2-plan.md#guardrails).
 *
 * After every edit or write under v2/, run `make check-v2` and attach any failure to that tool
 * result, so the agent sees it at once. Before a run ends, if v2/ (or the documents its rules
 * read) changed during the run and the check fails, hand the failures back and ask for another
 * turn, at most MAX_RETRIES times per prompt; then tell the operator it still fails.
 *
 * The rules themselves live in v2/archtest; this file only runs them. Pi loads it once the
 * project is trusted (/trust), or explicitly with `pi -e .pi/extensions/v2-guardrails.ts`.
 */
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { readdirSync, statSync } from "node:fs";
import { isAbsolute, join, relative, resolve, sep } from "node:path";

const MAX_RETRIES = 2;
const MAX_LINES = 80;
const TIMEOUT_MS = 180_000;
const WATCHED = ["v2", "docs/iteration-2-plan.md", "AGENTS.md"]; // the rules read the two documents

export default function (pi: ExtensionAPI) {
	let root: string | undefined;
	let baseline = "";
	let retries = 0;
	let queue: Promise<unknown> = Promise.resolve();

	async function repoRoot(cwd: string): Promise<string> {
		if (root) return root;
		const result = await pi.exec("git", ["rev-parse", "--show-toplevel"], { cwd, timeout: 5_000 });
		root = result.code === 0 ? result.stdout.trim() : cwd;
		return root;
	}

	/** Paths, sizes and modification times of everything watched: changes when any file does. */
	function fingerprint(top: string): string {
		const seen: string[] = [];
		const visit = (path: string) => {
			let info;
			try {
				info = statSync(path);
			} catch {
				return; // absent is a state too
			}
			if (info.isDirectory()) {
				for (const name of readdirSync(path).sort()) {
					if (!name.startsWith(".")) visit(join(path, name));
				}
			} else {
				seen.push(`${relative(top, path)}:${info.size}:${info.mtimeMs}`);
			}
		};
		for (const path of WATCHED) visit(join(top, path));
		return seen.join("\n");
	}

	function watched(top: string, cwd: string, path: unknown): boolean {
		if (typeof path !== "string") return false;
		const rel = relative(top, isAbsolute(path) ? path : resolve(cwd, path));
		return WATCHED.some((item) => rel === item || rel.startsWith(item + sep));
	}

	/** Run `make check-v2`, one at a time; on failure keep what explains it, not the passing packages. */
	function check(cwd: string): Promise<{ ok: boolean; text: string }> {
		const run = queue.then(async () => {
			const top = await repoRoot(cwd);
			const result = await pi.exec("make", ["--no-print-directory", "-s", "check-v2"], { cwd: top, timeout: TIMEOUT_MS });
			if (result.code === 0 && !result.killed) return { ok: true, text: "" };
			const lines = `${result.stdout}\n${result.stderr}`
				.split("\n")
				.filter((line) => line.trim() !== "" && !/^ok\s/.test(line) && !/^make(\[\d+\])?: \*\*\*/.test(line));
			const shown = lines.slice(0, MAX_LINES);
			if (lines.length > MAX_LINES) shown.push(`... ${lines.length - MAX_LINES} more lines: run make check-v2`);
			if (result.killed) shown.push(`make check-v2 did not finish within ${TIMEOUT_MS / 1000} s`);
			return { ok: false, text: shown.join("\n") };
		});
		queue = run.catch(() => undefined);
		return run;
	}

	pi.on("before_agent_start", async (event, ctx) => {
		if (event.messageType !== "user") return; // continuations keep their prompt's retry count
		retries = 0;
		baseline = fingerprint(await repoRoot(ctx.cwd));
	});

	pi.on("tool_result", async (event, ctx) => {
		if (event.isError || !/edit|write/i.test(event.toolName)) return;
		const top = await repoRoot(ctx.cwd);
		if (!watched(top, ctx.cwd, event.input.path ?? event.input.file_path)) return;
		const result = await check(ctx.cwd);
		const note = result.ok
			? "[v2 guardrails] make check-v2 passes after this edit."
			: "[v2 guardrails] make check-v2 fails after this edit. Fix the code before finishing; change a rule's " +
				"baseline only if the change is intended, and say why (docs/iteration-2-plan.md#guardrails):\n" + result.text;
		return { content: [...event.content, { type: "text", text: note }], structuredContent: event.structuredContent };
	});

	pi.on("agent_before_settle", async (event, ctx) => {
		if (event.outcome !== "completed") return;
		const top = await repoRoot(ctx.cwd);
		const now = fingerprint(top);
		if (now === baseline) return; // nothing the rules see changed during this prompt
		const result = await check(ctx.cwd);
		if (result.ok) {
			baseline = now;
			return;
		}
		if (retries >= MAX_RETRIES) {
			if (ctx.hasUI) ctx.ui.notify(`v2 guardrails: make check-v2 still fails after ${MAX_RETRIES} retries`, "warning");
			return {
				entries: [{
					type: "custom_message", customType: "v2-guardrails", display: true,
					content: `[v2 guardrails] make check-v2 still fails after ${MAX_RETRIES} attempts to fix it; ` +
						"the operator decides what happens next:\n" + result.text,
				}],
			};
		}
		retries += 1;
		return {
			entries: [{
				type: "custom_message", customType: "v2-guardrails", display: true,
				content: `[v2 guardrails] You changed v2 and make check-v2 fails (attempt ${retries} of ${MAX_RETRIES}). ` +
					"Fix what it reports before finishing; change a rule's baseline only if the change is intended, and " +
					"say why (docs/iteration-2-plan.md#guardrails):\n" + result.text,
			}],
			continue: true,
		};
	});
}
