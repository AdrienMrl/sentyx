"""Small atomic status writer consumed by the LAN training dashboard."""

import json
import os
import tempfile
import time


class Writer:
    def __init__(self, path, phase, **initial):
        self.path = path or os.environ.get("VJEPA_PROGRESS", "")
        self.started = time.time()
        self.state = {"schema_version": 1, "state": "running", "phase": phase,
                      "started_unix": self.started, **initial}
        self.write()

    def write(self, **values):
        if not self.path:
            return
        self.state.update(values)
        self.state["updated_unix"] = time.time()
        os.makedirs(os.path.dirname(os.path.abspath(self.path)), exist_ok=True)
        directory = os.path.dirname(os.path.abspath(self.path))
        fd, temp = tempfile.mkstemp(prefix=os.path.basename(self.path) + ".", suffix=".tmp", dir=directory)
        try:
            with os.fdopen(fd, "w") as f:
                json.dump(self.state, f, indent=1)
                f.write("\n")
            # Windows may deny replacement for the few milliseconds in which
            # the dashboard has the destination open. Retrying keeps telemetry
            # from being able to terminate the actual training job.
            for attempt in range(100):
                try:
                    os.replace(temp, self.path)
                    break
                except PermissionError:
                    if attempt == 99:
                        raise
                    time.sleep(0.05)
        finally:
            if os.path.exists(temp):
                os.unlink(temp)

    def complete(self, **values):
        self.write(state="complete", elapsed_s=time.time() - self.started, **values)

    def fail(self, error):
        self.write(state="failed", elapsed_s=time.time() - self.started, error=str(error))
