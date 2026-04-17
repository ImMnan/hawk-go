
## Basic functionality of HAWK 

### Primary functions

- `main()` which is going to execute the package `pkg` > `Run()`
- `Run()` which is going to execute the main logic of the package, which will be basic go routine in an infinite for loop
    - RUN will consist of init and the main `for` loop. 
      
    - Init function that will initiate the config reader
        - `readConfig()` which is going to read the `yaml` configuration file and update the local memory cache. 
	- the configurations needs to be updated/wrote into struct or a constant
        - Read ENV variables and process them as needed for further program executions. 

    - The main `for` loop which is designed to just run through list of configs and execute them, never to loop back again. 
      It is supposed to break the loop, when there is value retured from the go routines, executing individual syncs. 
 
    - The go routines are going to be:
        - Iterate over each item in the configuration file, as different item may have different sync cycles and different git repositories.
            - run sync functions which is going to check the git repository for the latest commits and diff it with the last one. 
                - It will first match the last synced commit/batch from mongo last commit (if present)(else run the match against last commit in the repo for that main branch), 
		- Hawk will then compare/diff them against the new commits/batches.
                - Program will then sort and bundle the pages that had the changes. 
                - Then it will add these pages, old and new to existing directory structure for the other tool to use. 
            - Once this is done, it will come back to the main for loop and execute the next iteration
            - Read the cache and wait/sleep as per the sync cycle. 


```
sync(c)
 │
 ├─ guard: if sync.enabled == false → return immediately
 │
 ├─ calls syncTrigger(c)
 │   └─ returns: trigger channel, stop func, err
 │
 └─ for triggeredAt := range trigger {
         ← BLOCKS here until cron fires
         → runs one sync pass over all sources
         ← BLOCKS again for next cron tick
    }
```


```
cronSched.Start()  ← background goroutine managed by the cron library

every time cron schedule fires:
   func() {
      select {
        case trigger <- time.Now():   ← wake up sync()
        default:                       ← drop tick if sync() is still busy
      }
   }

stop() called:
   cronSched.Stop().Done()  ← waits for in-flight cron job to finish
   close(trigger)           ← breaks the "for range" in sync(), goroutine exits
```