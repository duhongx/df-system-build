export const quoteIdentifier = value => '"' + String(value).replaceAll('"','""') + '"'
export const relationQuery = relation => `SELECT *\nFROM ${quoteIdentifier(relation.schema)}.${quoteIdentifier(relation.name)};`
export const formatDuration = ms => ms == null ? '—' : ms < 1000 ? `${Number(ms).toFixed(1)} ms` : ms < 60000 ? `${(ms/1000).toFixed(2)} s` : `${(ms/60000).toFixed(1)} min`
export const formatDate = text => text ? new Date(text).toLocaleString('zh-CN',{hour12:false}) : '—'
export const targetLabel = db => db ? `${db.username}@${db.host}:${db.port}/${db.database}` : '连接已移除'
// A log source may contain every database on the server. Never bind a sample to an unrelated database.
export const sampleTarget = (source, database, connections) => !database ? undefined : connections.find(db => db.host===source.host && db.port===source.port && db.database===database)
export function planRows(plan, depth=0, output=[]){
  if(!plan)return output
  output.push({node:plan,depth,id:output.length})
  for(const child of plan.Plans||[])planRows(child,depth+1,output)
  return output
}
