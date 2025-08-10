# UseShortHashLen

# ShortHashLen can NOT be changed after hashdb creation.
# One should think wisely before creating the hashdb.
# Changing this value later will require a complete rebuild of the history database, which not really tested yet.

**Hash routing breakdown:**
- **1st char**: Database selection (0-f) (16 hash databases)
- **2nd+3rd chars**: Table selection (s00-sff) (256 tables per database)
- **4th-6th chars**: short hash (default UseShortHashLen=3)
- **Total**: Default UseShortHashLen = 3 + 3 (db[0-f]:table[s00-sff]) = 6 characters = 16^6 = **16,777,216 combinations**


ShortHashLen:
- "2" (+ 3 for db[0-f]:table[s00-sff] selection) total of 5 chars = 16^5 combinations = 1,048,576)
- "3" (+ 3 for db[0-f]:table[s00-sff] selection) total of 6 chars = 16^6 combinations = 16,777,216)
- "4" (+ 3 for db[0-f]:table[s00-sff] selection) total of 7 chars = 16^7 combinations = 268,435,456)
- "5" (+ 3 for db[0-f]:table[s00-sff] selection) total of 8 chars = 16^8 combinations = 4,294,967,296)
- "6" (+ 3 for db[0-f]:table[s00-sff] selection) total of 9 chars = 16^9 combinations = 68,719,476,736)
- "7" (+ 3 for db[0-f]:table[s00-sff] selection) total of 10 chars = 16^10 combinations = 1,099,511,627,776)


### Collision Example: UseShortHashLen=2 with 1M articles

**The math:**
- UseShortHashLen=2 = 1,048,576 possible hash slots
- 1M articles = nearly 100% capacity utilization
- Result: Many collisions, but system handles them gracefully

**What happens in practice:**
- Many hash entries store multiple comma-separated file offsets
- Example: A single hash might store "12345,23456,34567,45678" (4 colliding articles)
- During lookups, the system reads all 4 positions from history.dat and verifies the full hash of each to find the correct article
- **Performance impact**: Instead of 1 disk read, many lookups require 3-5+ disk reads

**Why performance degrades:**
- With 1M articles in 1M slots, frequent collisions occur
- Each collision increases lookup time proportionally
- The system works correctly, but slower due to additional I/O operations

The collision handling is robust and reliable - it's a performance trade-off, not a failure mode.

### Implementation Background

This hash-based history system follows design principles similar to traditional Usenet news servers, particularly inspired by approaches used in systems like INN2 (InterNetNews). The core concept involves:

**Traditional Usenet History Approach:**
- Hash-based duplicate detection using message-id hashes
- File offset storage for quick article retrieval
- Sharded databases to distribute load and improve concurrency
- Collision handling through chaining (multiple offsets per hash)

**Our Implementation:**
- **Hash Routing**: Uses first 3 characters for database/table selection (16 DBs × 256 tables = 4,096 shards)
- **Storage Hash**: Configurable UseShortHashLen for collision resistance vs. storage efficiency
- **File Offsets**: Stores byte positions in history.dat for direct article access
- **Collision Management**: Comma-separated offsets with full hash verification during lookups
- **Graceful Degradation**: System remains functional under high collision scenarios

The sharding strategy (16 databases × 256 tables each) provides excellent distribution and allows for high-concurrency operations while maintaining the simplicity and reliability that traditional Usenet history systems are known for.


