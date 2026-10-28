select   
     datname "数据库名称",  
     age(datfrozenxid) "数据库年龄"
from pg_database 
where datname <> 'template1'
    and datname <> 'template0'  
order by 2 desc;
