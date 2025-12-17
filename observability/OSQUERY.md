# osquery Usage

osquery exposes the operating system as a relational database. You can query system state using SQL.

## Interactive Shell

SSH to a host and run:

```bash
sudo osqueryi
```

## Basic Queries

### System Info
```sql
SELECT * FROM system_info;
```

### Running Processes
```sql
SELECT pid, name, cmdline, resident_size FROM processes ORDER BY resident_size DESC LIMIT 10;
```

### Listening Ports
```sql
SELECT p.pid, p.name, l.port, l.address, l.protocol
FROM listening_ports l
JOIN processes p ON l.pid = p.pid
WHERE l.port != 0;
```

### Logged In Users
```sql
SELECT * FROM logged_in_users;
```

### Disk Usage
```sql
SELECT * FROM mounts;
```

### Docker Containers
```sql
SELECT * FROM docker_containers;
```

### Open Files
```sql
SELECT p.name, p.pid, pof.path
FROM process_open_files pof
JOIN processes p ON pof.pid = p.pid
LIMIT 20;
```

### Network Connections
```sql
SELECT p.name, p.pid, s.local_address, s.local_port, s.remote_address, s.remote_port, s.state
FROM process_open_sockets s
JOIN processes p ON s.pid = p.pid
WHERE s.remote_port != 0;
```

### Cron Jobs
```sql
SELECT * FROM crontab;
```

### Users
```sql
SELECT * FROM users;
```

## List Available Tables

```sql
.tables
```

## Describe a Table

```sql
.schema processes
```
