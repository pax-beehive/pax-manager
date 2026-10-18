// Bound authenticated native bootstrap/logout load per gateway process.
export function nativeLimiter(limit = 120, capacity = 10000, clock = Date.now) {
  const buckets = new Map<string, { window: number; count: number }>();
  return (subject: string) => {
    const window = Math.floor(clock() / 60000);
    let bucket = buckets.get(subject);
    if (!bucket || bucket.window !== window) {
      if (buckets.size >= capacity) {
        for (const [key, value] of buckets)
          if (value.window !== window) buckets.delete(key);
        if (buckets.size >= capacity) return false;
      }
      bucket = { window, count: 0 };
      buckets.set(subject, bucket);
    }
    if (bucket.count >= limit) return false;
    bucket.count++;
    return true;
  };
}
