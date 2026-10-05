package redis

import goredis "github.com/redis/go-redis/v9"

// Redis TIME is the authority for expiry and capacity scores across machines.
const redisTime = `local t = redis.call('TIME'); local now = t[1]*1000 + math.floor(t[2]/1000)
`

var leaseScript = goredis.NewScript(redisTime + `
local old = redis.call('GET', KEYS[1])
local worker = cjson.decode(ARGV[1])
if ARGV[3] == 'renew' and not old then return -1 end
if old and cjson.decode(old).token ~= worker.token then return -1 end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now)
redis.call('ZADD', KEYS[2], now + tonumber(ARGV[2]), worker.id)
return 1
`)

var unregisterScript = goredis.NewScript(`
local old = redis.call('GET', KEYS[1])
if old and cjson.decode(old).token == ARGV[1] then
 redis.call('DEL', KEYS[1]); redis.call('ZREM', KEYS[2], ARGV[2]); return 1
end
return 0
`)

var reserveScript = goredis.NewScript(redisTime + `
-- Return the same reservation if a caller safely repeats the same ID.
local existing = redis.call('GET', KEYS[2])
if existing then
 if cjson.decode(existing).client_id == ARGV[2] then return existing end
 return redis.error_reply('session ID collision')
end
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
local ids = redis.call('ZRANGE', KEYS[1], 0, -1)
local best = nil; local ratio = 2
for _, id in ipairs(ids) do
 local data = redis.call('GET', ARGV[1] .. 'worker:' .. id)
 if data then
  local worker = cjson.decode(data)
  local slots = ARGV[1] .. 'slots:' .. worker.id .. ':' .. worker.token
  redis.call('ZREMRANGEBYSCORE', slots, '-inf', now)
  local count = redis.call('ZCARD', slots)
  if count < worker.capacity and count/worker.capacity < ratio then best = worker; ratio = count/worker.capacity end
 end
end
if not best then return nil end
local expires = now + tonumber(ARGV[4])
local record = {id=ARGV[3],client_id=ARGV[2],worker_id=best.id,worker_token=best.token,worker_url=best.url,state='starting',created_at_ms=now,expires_at_ms=expires}
local encoded = cjson.encode(record)
local slots = ARGV[1] .. 'slots:' .. best.id .. ':' .. best.token
redis.call('SET', KEYS[2], encoded, 'PX', ARGV[4])
redis.call('ZADD', slots, expires, record.id)
local lifetime = tonumber(ARGV[4])+1000
if redis.call('PTTL', slots) < lifetime then redis.call('PEXPIRE', slots, lifetime) end
return encoded
`)

var lookupScript = goredis.NewScript(`
local data = redis.call('GET', KEYS[1]); if not data then return nil end
local record = cjson.decode(data)
if record.client_id ~= ARGV[2] or record.state ~= ARGV[3] then return nil end
local worker = redis.call('GET', ARGV[1] .. 'worker:' .. record.worker_id)
if not worker or cjson.decode(worker).token ~= record.worker_token then return nil end
return data
`)

var publishScript = goredis.NewScript(redisTime + `
local data = redis.call('GET', KEYS[1]); if not data then return 0 end
local old = cjson.decode(data)
local worker = redis.call('GET', KEYS[2])
if not worker or cjson.decode(worker).token ~= ARGV[1] or old.worker_token ~= ARGV[1] or old.client_id ~= ARGV[2] or old.state ~= 'starting' then return 0 end
old.state = 'ready'; old.created_at_ms = tonumber(ARGV[3]); old.expires_at_ms = tonumber(ARGV[4])
redis.call('SET', KEYS[1], cjson.encode(old), 'PX', ARGV[5])
redis.call('ZADD', KEYS[3], now + tonumber(ARGV[5]), old.id)
local lifetime = tonumber(ARGV[5])+1000
if redis.call('PTTL', KEYS[3]) < lifetime then redis.call('PEXPIRE', KEYS[3], lifetime) end
return 1
`)

var releaseScript = goredis.NewScript(`
local data = redis.call('GET', KEYS[1])
if data then
 local old = cjson.decode(data)
 if old.worker_token ~= ARGV[1] or old.client_id ~= ARGV[2] then return 0 end
 redis.call('DEL', KEYS[1])
end
redis.call('ZREM', KEYS[2], ARGV[3])
return 1
`)
