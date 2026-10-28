<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, watch } from "vue";
import { basicSetup } from "codemirror";
import { EditorState, Compartment } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { sql, PostgreSQL, type SQLNamespace } from "@codemirror/lang-sql";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { tags } from "@lezer/highlight";
const props = defineProps<{
  modelValue: string;
  schema?: SQLNamespace;
  defaultSchema?: string;
  active?: boolean;
}>();
const emit = defineEmits<{
  "update:modelValue": [string];
  run: [string];
  explain: [string];
  error: [string];
  save: [];
}>();
const host = ref<HTMLElement>();
let view: EditorView | undefined;
const dialect = new Compartment();
async function formatSelection() {
  if (!view) return;
  const before = view.state;
  const selection = before.selection.main;
  const from = selection.empty ? 0 : selection.from,
    to = selection.empty ? before.doc.length : selection.to;
  try {
    const { format } = await import("sql-formatter");
    const text = format(before.sliceDoc(from, to), {
      language: "postgresql",
      keywordCase: "upper",
      tabWidth: 2,
    });
    if (!view || view.state !== before) {
      emit("error", "内容已变化，请重新格式化");
      return;
    }
    view.dispatch({
      changes: { from, to, insert: text },
      selection: { anchor: from, head: from + text.length },
      userEvent: "input.format",
    });
    view.focus();
  } catch (e: any) {
    emit("error", `SQL 格式化失败：${e.message}`);
  }
}
const selected = () =>
  view?.state
    .sliceDoc(view.state.selection.main.from, view.state.selection.main.to)
    .trim() ||
  view?.state.doc.toString() ||
  "";
onMounted(() => {
  view = new EditorView({
    parent: host.value!,
    state: EditorState.create({
      doc: props.modelValue,
      extensions: [
        basicSetup,
        syntaxHighlighting(
          HighlightStyle.define([
            { tag: tags.keyword, color: "var(--ui-primary)" },
            { tag: tags.string, color: "var(--el-color-success)" },
            { tag: tags.number, color: "var(--el-color-warning)" },
            {
              tag: tags.comment,
              color: "var(--ui-muted)",
              fontStyle: "italic",
            },
            { tag: [tags.operator, tags.punctuation], color: "var(--ui-text)" },
          ]),
        ),
        keymap.of([
          {
            key: "Mod-s",
            run: () => {
              emit("save");
              return true;
            },
          },
          {
            key: "Mod-Shift-f",
            run: () => {
              void formatSelection();
              return true;
            },
          },
          {
            key: "Mod-Enter",
            run: () => {
              emit("run", selected());
              return true;
            },
          },
          {
            key: "Mod-Shift-Enter",
            run: () => {
              emit("explain", selected());
              return true;
            },
          },
        ]),
        dialect.of(
          sql({
            dialect: PostgreSQL,
            schema: props.schema,
            defaultSchema: props.defaultSchema,
            upperCaseKeywords: true,
          }),
        ),
        EditorView.updateListener.of((update) => {
          if (update.docChanged)
            emit("update:modelValue", update.state.doc.toString());
        }),
        EditorView.theme({
          "&": {
            height: "100%",
            fontSize: "13px",
            backgroundColor: "var(--ui-panel)",
            color: "var(--ui-heading)",
          },
          ".cm-scroller": {
            fontFamily:
              "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
            overflow: "auto",
          },
          ".cm-content": { padding: "14px 0", caretColor: "var(--ui-primary)" },
          ".cm-gutters": {
            backgroundColor: "var(--ui-panel-soft)",
            color: "var(--ui-muted)",
            borderRight: "1px solid var(--ui-border)",
          },
          ".cm-activeLine, .cm-activeLineGutter": {
            backgroundColor: "var(--ui-tint)",
          },
          "&.cm-focused": { outline: "none" },
          ".cm-tooltip": {
            backgroundColor: "var(--ui-panel)",
            color: "var(--ui-heading)",
            border: "1px solid var(--ui-border)",
          },
          ".cm-cursor": { borderLeftColor: "var(--ui-primary)" },
          ".cm-selectionBackground, &.cm-focused .cm-selectionBackground": {
            backgroundColor: "var(--el-color-primary-light-8)",
          },
        }),
      ],
    }),
  });
});
watch(
  () => props.modelValue,
  (value) => {
    if (view && view.state.doc.toString() !== value)
      view.dispatch({
        changes: { from: 0, to: view.state.doc.length, insert: value },
      });
  },
);
watch(
  () => [props.schema, props.defaultSchema],
  () =>
    view?.dispatch({
      effects: dialect.reconfigure(
        sql({
          dialect: PostgreSQL,
          schema: props.schema,
          defaultSchema: props.defaultSchema,
          upperCaseKeywords: true,
        }),
      ),
    }),
);
onBeforeUnmount(() => view?.destroy());
watch(
  () => props.active,
  (value) => {
    if (value) view?.requestMeasure();
  },
);
defineExpose({ selected, formatSelection, focus: () => view?.focus() });
</script>
<template><div ref="host" class="sql-editor" /></template>
<style scoped>
.sql-editor {
  height: 100%;
  min-height: 180px;
  overflow: hidden;
  text-align: left;
}
</style>
