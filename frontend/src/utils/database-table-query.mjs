import { quoteIdentifier } from "./database-workbench.mjs";
export function tableQuery(relation, columns, options = {}) {
  const names = new Set(columns.map((c) => c.name));
  const selected = (options.columns || columns.map((c) => c.name)).filter((n) =>
    names.has(n),
  );
  if (!selected.length) throw new Error("至少选择一列");
  const parameters = [];
  let sql = `SELECT ${selected.map(quoteIdentifier).join(", ")}\nFROM ${quoteIdentifier(relation.schema)}.${quoteIdentifier(relation.name)}`;
  if (options.filterColumn) {
    if (!names.has(options.filterColumn)) throw new Error("筛选列已变化");
    const col = quoteIdentifier(options.filterColumn),
      op = options.operator || "=";
    if (op === "IS NULL" || op === "IS NOT NULL") sql += `\nWHERE ${col} ${op}`;
    else if (op === "contains") {
      parameters.push(
        "%" + String(options.value ?? "").replace(/[\\%_]/g, "\\$&") + "%",
      );
      sql += `\nWHERE ${col}::text ILIKE $1 ESCAPE '\\'`;
    } else {
      if (!["=", "<>", "<", "<=", ">", ">="].includes(op))
        throw new Error("筛选操作符无效");
      parameters.push(String(options.value ?? ""));
      sql += `\nWHERE ${col} ${op} $1`;
    }
  }
  const sorted = [];
  if (options.sortColumn) {
    if (!names.has(options.sortColumn)) throw new Error("排序列已变化");
    sorted.push(
      quoteIdentifier(options.sortColumn) + (options.desc ? " DESC" : " ASC"),
    );
  }
  for (const col of columns)
    if (col.primaryKey && col.name !== options.sortColumn)
      sorted.push(quoteIdentifier(col.name) + " ASC");
  if (sorted.length) sql += "\nORDER BY " + sorted.join(", ");
  const size = Number(options.pageSize || 100),
    page = Number(options.page || 1);
  if (
    !Number.isSafeInteger(size) ||
    size < 1 ||
    size > 1000 ||
    !Number.isSafeInteger(page) ||
    page < 1 ||
    !Number.isSafeInteger((page - 1) * size)
  )
    throw new Error("分页参数无效");
  sql += `\nLIMIT ${size + 1} OFFSET ${(page - 1) * size};`;
  return { sql, parameters };
}
