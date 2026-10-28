import { ref, watch, onBeforeUnmount } from "vue";
import {
  loadWorkspace,
  saveWorkspace,
  type WorkspaceState,
} from "../api/database-workbench";

export function useDatabaseWorkspace(
  snapshot: () => WorkspaceState,
  apply: (state: WorkspaceState) => void,
) {
  const loaded = ref(false),
    initializing = ref(false),
    saving = ref(false),
    conflict = ref(false),
    error = ref(""),
    dirty = ref(false),
    updatedAt = ref<string | null>(null);
  let revision = 0,
    lastSaved = "",
    timer: ReturnType<typeof setTimeout> | undefined,
    inFlight: Promise<void> | undefined;
  const clear = () => {
    if (timer) clearTimeout(timer);
    timer = undefined;
  };
  async function initialize() {
    initializing.value = true;
    error.value = "";
    try {
      const value = await loadWorkspace();
      revision = value.revision;
      apply(value.state);
      lastSaved = JSON.stringify(snapshot());
      updatedAt.value = value.updatedAt;
      dirty.value = false;
      conflict.value = false;
      loaded.value = true;
    } catch (e: any) {
      error.value = e.message;
      throw e;
    } finally {
      initializing.value = false;
    }
  }
  function flush(): Promise<void> {
    clear();
    if (inFlight) return inFlight;
    if (!loaded.value || conflict.value || !dirty.value)
      return Promise.resolve();
    inFlight = (async () => {
      saving.value = true;
      error.value = "";
      try {
        while (dirty.value && !conflict.value) {
          const content = JSON.stringify(snapshot());
          const value = await saveWorkspace(JSON.parse(content), revision);
          revision = value.revision;
          lastSaved = content;
          updatedAt.value = value.updatedAt;
          dirty.value = JSON.stringify(snapshot()) !== lastSaved;
        }
      } catch (e: any) {
        error.value = e.message;
        conflict.value = !!e.conflict;
      } finally {
        saving.value = false;
        inFlight = undefined;
      }
    })();
    return inFlight;
  }
  async function overwrite() {
    if (inFlight) await inFlight;
    const remote = await loadWorkspace();
    revision = remote.revision;
    conflict.value = false;
    loaded.value = true;
    dirty.value = true;
    await flush();
  }
  watch(
    () => JSON.stringify(snapshot()),
    (value) => {
      if (initializing.value) return;
      if (!loaded.value) {
        dirty.value = snapshot().tabs.length > 0;
        return;
      }
      dirty.value = value !== lastSaved;
      clear();
      if (dirty.value && !conflict.value) {
        timer = setTimeout(() => void flush(), 800);
      }
    },
  );
  const unload = (event: BeforeUnloadEvent) => {
    if (dirty.value) {
      event.preventDefault();
      event.returnValue = "";
    }
  };
  const hidden = () => {
    if (document.visibilityState === "hidden") void flush();
  };
  window.addEventListener("beforeunload", unload);
  document.addEventListener("visibilitychange", hidden);
  onBeforeUnmount(() => {
    clear();
    window.removeEventListener("beforeunload", unload);
    document.removeEventListener("visibilitychange", hidden);
    void flush();
  });
  return {
    loaded,
    initializing,
    saving,
    conflict,
    error,
    dirty,
    updatedAt,
    initialize,
    flush,
    overwrite,
  };
}
