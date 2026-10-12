export function formatCount(value: number) {
  return value.toLocaleString()
}

export function formatDateTime(value: string | number) {
  return new Date(value).toLocaleString(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  })
}

export function formatCreatedTime(value: string | number) {
  return new Date(value).toLocaleString("en-US", {
    dateStyle: "medium",
    timeStyle: "short",
  })
}

export function formatLabel(value: string) {
  return value
    .replace(/[_-]+/g, " ")
    .replace(/\b[a-z]/g, (letter) => letter.toUpperCase())
}
