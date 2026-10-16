// Keep the workbench order consistent with the backend's numeric version sort.
export function compareSQLVersions(first: string, second: string): number {
  let a = first, b = second;
  while (a.length && b.length) {
    if (/^[0-9]/.test(a) && /^[0-9]/.test(b)) {
      const left = a.match(/^[0-9]+/)![0], right = b.match(/^[0-9]+/)![0];
      const x = left.replace(/^0+/, ''), y = right.replace(/^0+/, '');
      if (x.length !== y.length) return x.length < y.length ? -1 : 1;
      if (x !== y) return x < y ? -1 : 1;
      a = a.slice(left.length); b = b.slice(right.length);
    } else {
      if (a[0] !== b[0]) return a[0]! < b[0]! ? -1 : 1;
      a = a.slice(1); b = b.slice(1);
    }
  }
  return a === b ? 0 : a < b ? -1 : 1;
}
export function compareSQLFiles(a: {version: string; groupSortNo: number; fileName: string; id: number}, b: {version: string; groupSortNo: number; fileName: string; id: number}): number {
  const version = compareSQLVersions(a.version, b.version);
  if (version) return version;
  if (a.groupSortNo !== b.groupSortNo) return a.groupSortNo - b.groupSortNo;
  if (a.fileName !== b.fileName) return a.fileName < b.fileName ? -1 : 1;
  return a.id - b.id;
}
