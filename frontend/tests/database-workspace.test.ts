import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createApp, h, nextTick, reactive, type App } from "vue";
import type {
  WorkspaceResponse,
  WorkspaceState,
} from "../src/api/database-workbench";
import { useDatabaseWorkspace } from "../src/composables/useDatabaseWorkspace";

const api = vi.hoisted(() => ({
  loadWorkspace: vi.fn(),
  saveWorkspace: vi.fn(),
}));
vi.mock("../src/api/database-workbench", () => api);

function state(): WorkspaceState {
  return {
    version: 1,
    tabs: [
      {
        id: 1,
        name: "SQL 1",
        databaseId: 9,
        revision: 7,
        target: "server:5432/app",
        schema: "business",
        sql: "select 1",
        parameters: "[]",
        maxRows: 1000,
        timeoutSeconds: 15,
      },
    ],
    closedTabs: [],
    activeTabId: 1,
    databaseId: 9,
    pane: "sql",
    editorHeight: 245,
    navigationOpen: true,
  };
}
function response(value: WorkspaceState, revision = 3): WorkspaceResponse {
  return {
    state: structuredClone(value),
    revision,
    updatedAt: "2026-10-28T00:00:00Z",
  };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}
let app: App | undefined;
function mount() {
  const draft = reactive(state());
  let workspace!: ReturnType<typeof useDatabaseWorkspace>;
  app = createApp({
    setup() {
      workspace = useDatabaseWorkspace(
        () => draft,
        (value) => Object.assign(draft, value),
      );
      return () => h("div");
    },
  });
  const host = document.createElement("div");
  document.body.append(host);
  app.mount(host);
  return { draft, workspace };
}
beforeEach(() => {
  vi.useFakeTimers();
  api.loadWorkspace.mockReset().mockResolvedValue(response(state()));
  api.saveWorkspace
    .mockReset()
    .mockImplementation(async (value, revision) =>
      response(value, revision + 1),
    );
});
afterEach(async () => {
  app?.unmount();
  app = undefined;
  await Promise.resolve();
  document.body.replaceChildren();
  vi.useRealTimers();
});

describe("database workspace persistence", () => {
  it("restores drafts and target revision without saving or executing anything", async () => {
    const remote = state();
    remote.tabs[0]!.sql = "select 42";
    remote.closedTabs = [{ ...remote.tabs[0]!, id: 2, name: "closed SQL" }];
    api.loadWorkspace.mockResolvedValue(response(remote));
    const { draft, workspace } = mount();
    await workspace.initialize();
    await nextTick();
    await vi.advanceTimersByTimeAsync(1000);
    expect(draft.tabs[0]).toMatchObject({
      sql: "select 42",
      revision: 7,
      target: "server:5432/app",
    });
    expect(draft.closedTabs[0]!.name).toBe("closed SQL");
    expect(workspace.dirty.value).toBe(false);
    expect(api.saveWorkspace).not.toHaveBeenCalled();
  });

  it("debounces and serializes saves, then saves edits made during an in-flight request", async () => {
    const { draft, workspace } = mount();
    await workspace.initialize();
    await nextTick();
    const first = deferred<WorkspaceResponse>();
    api.saveWorkspace.mockImplementationOnce(() => first.promise);
    draft.tabs[0]!.sql = "select 2";
    await nextTick();
    await vi.advanceTimersByTimeAsync(799);
    expect(api.saveWorkspace).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(api.saveWorkspace).toHaveBeenCalledTimes(1);
    expect(api.saveWorkspace.mock.calls[0]![1]).toBe(3);
    draft.tabs[0]!.sql = "select 3";
    await nextTick();
    await vi.advanceTimersByTimeAsync(1000);
    expect(api.saveWorkspace).toHaveBeenCalledTimes(1);
    first.resolve(response(api.saveWorkspace.mock.calls[0]![0], 4));
    await workspace.flush();
    expect(api.saveWorkspace).toHaveBeenCalledTimes(2);
    expect(api.saveWorkspace.mock.calls[1]![0].tabs[0].sql).toBe("select 3");
    expect(api.saveWorkspace.mock.calls[1]![1]).toBe(4);
    expect(draft.tabs[0]!.sql).toBe("select 3");
    expect(workspace.dirty.value).toBe(false);
  });

  it("keeps local edits on conflict and only overwrites after explicitly acquiring the latest revision", async () => {
    const { draft, workspace } = mount();
    await workspace.initialize();
    await nextTick();
    api.saveWorkspace.mockRejectedValueOnce(
      Object.assign(new Error("版本冲突"), { conflict: true }),
    );
    draft.tabs[0]!.sql = "select local";
    await nextTick();
    await workspace.flush();
    expect(workspace.conflict.value).toBe(true);
    draft.tabs[0]!.sql = "select newer_local";
    await nextTick();
    await vi.advanceTimersByTimeAsync(2000);
    expect(api.saveWorkspace).toHaveBeenCalledTimes(1);
    const remote = state();
    remote.tabs[0]!.sql = "select remote";
    api.loadWorkspace.mockResolvedValue(response(remote, 9));
    await workspace.overwrite();
    expect(api.saveWorkspace.mock.calls[1]![1]).toBe(9);
    expect(api.saveWorkspace.mock.calls[1]![0].tabs[0].sql).toBe(
      "select newer_local",
    );
    expect(draft.tabs[0]!.sql).toBe("select newer_local");
    expect(workspace.conflict.value).toBe(false);
    expect(workspace.dirty.value).toBe(false);
  });

  it("retains failed saves for retry and warns on leaving only while dirty", async () => {
    const { draft, workspace } = mount();
    await workspace.initialize();
    await nextTick();
    api.saveWorkspace.mockRejectedValueOnce(new Error("offline"));
    draft.tabs[0]!.sql = "select unsaved";
    await nextTick();
    await workspace.flush();
    expect(workspace.error.value).toBe("offline");
    const dirtyEvent = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(dirtyEvent);
    expect(dirtyEvent.defaultPrevented).toBe(true);
    expect(draft.tabs[0]!.sql).toBe("select unsaved");
    await workspace.flush();
    expect(workspace.dirty.value).toBe(false);
    expect(workspace.error.value).toBe("");
    const cleanEvent = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(cleanEvent);
    expect(cleanEvent.defaultPrevented).toBe(false);
  });

  it("does not overwrite unread remote drafts after a failed initial load", async () => {
    api.loadWorkspace.mockRejectedValue(new Error("offline"));
    const { draft, workspace } = mount();
    await expect(workspace.initialize()).rejects.toThrow("offline");
    draft.tabs[0]!.sql = "select local_after_failure";
    await nextTick();
    await vi.advanceTimersByTimeAsync(2000);
    await workspace.flush();
    expect(workspace.dirty.value).toBe(true);
    expect(api.saveWorkspace).not.toHaveBeenCalled();
    const event = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
  });
});
