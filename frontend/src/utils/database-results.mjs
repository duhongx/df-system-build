export function resultRows(
  result,
  keyword = "",
  filter = false,
  sortIndex = -1,
  order = "ascending",
) {
  const text = keyword.toLocaleLowerCase();
  let rows = (result?.rows || []).map((values, index) => ({
    index: index + 1,
    values,
  }));
  if (filter && text)
    rows = rows.filter((r) =>
      r.values.some(
        (v) => v !== null && String(v).toLocaleLowerCase().includes(text),
      ),
    );
  if (sortIndex >= 0)
    rows.sort((a, b) => {
      const x = a.values[sortIndex],
        y = b.values[sortIndex];
      if (x === null) return y === null ? 0 : 1;
      if (y === null) return -1;
      const type = result.columns[sortIndex]?.type || "";
      let compared;
      if (/^(int[248]|numeric|float[48]|money)$/.test(type))
        compared = compareDecimal(String(x), String(y));
      if (compared === undefined)
        compared = String(x).localeCompare(String(y), "zh-CN", {
          numeric: true,
        });
      return (order === "descending" ? -1 : 1) * compared || a.index - b.index;
    });
  return rows;
}
function decimal(value) {
  const m = /^([+-]?)(\d+)(?:\.(\d*))?(?:[eE]([+-]?\d+))?$/.exec(value);
  if (!m) return;
  let digits = (m[2] + (m[3] || "")).replace(/^0+/, "");
  if (!digits) return { sign: 0, digits: "0", power: 0 };
  const exponent = Number(m[4] || 0);
  if (!Number.isSafeInteger(exponent) || Math.abs(exponent) > 100000) return;
  const power = digits.length - (m[3] || "").length + exponent;
  return {
    sign: m[1] === "-" ? -1 : 1,
    digits: digits.replace(/0+$/, ""),
    power,
  };
}
export function compareDecimal(x, y) {
  const a = decimal(x),
    b = decimal(y);
  if (!a || !b) return;
  if (a.sign !== b.sign) return a.sign - b.sign;
  if (!a.sign) return 0;
  if (a.power !== b.power) return (a.power - b.power) * a.sign;
  const n = Math.max(a.digits.length, b.digits.length);
  return (
    a.digits.padEnd(n, "0").localeCompare(b.digits.padEnd(n, "0")) * a.sign
  );
}
export function delimited(columns, rows, separator = ",") {
  const cell = (v) => {
    const text = v === null ? "\\N" : String(v ?? "");
    return text.includes(separator) || /["\r\n]/.test(text)
      ? '"' + text.replaceAll('"', '""') + '"'
      : text;
  };
  return (
    [
      columns.map((c) => c.name),
      ...rows.map((r) => (Array.isArray(r) ? r : r.values)),
    ]
      .map((row) => row.map(cell).join(separator))
      .join("\r\n") + "\r\n"
  );
}
export function textParts(value, keyword) {
  const text = String(value ?? "");
  if (!keyword) return [{ text, match: false }];
  const lower = text.toLocaleLowerCase(),
    term = keyword.toLocaleLowerCase(),
    out = [];
  let start = 0,
    index;
  while ((index = lower.indexOf(term, start)) >= 0) {
    if (index > start)
      out.push({ text: text.slice(start, index), match: false });
    out.push({ text: text.slice(index, index + keyword.length), match: true });
    start = index + keyword.length;
  }
  if (start < text.length) out.push({ text: text.slice(start), match: false });
  return out;
}
