select     
   slot_name "slot_name(复制槽名称)",    
   plugin "plugin(插件名称)",    
   slot_type "slot_type(复制槽类型)",    
   active "state(复制槽状态)",case     
       when active = 't' then '复制槽正常'    
       when active = 'f' then '复制槽失败'    
        end "state(复制槽状态)"  
from pg_replication_slots;
