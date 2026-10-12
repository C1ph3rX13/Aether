"use client"

import { useCallback } from "react"

// Native focus scrolling must account for the page's sticky controls, including
// their changing height when copy wraps or the operator increases text size.
export function useScrollPadding() {
  return useCallback((element: HTMLElement | null) => {
    if (!element) return
    const scrollContainer = document.documentElement

    const property = "--page-scroll-end"
    const previous = scrollContainer.style.getPropertyValue(property)
    const update = () => {
      const sticky = getComputedStyle(element).position === "sticky"
      scrollContainer.style.setProperty(
        property,
        sticky
          ? `${Math.ceil(element.getBoundingClientRect().height) + 16}px`
          : "0px"
      )
    }
    const observer = new ResizeObserver(update)
    observer.observe(element)
    update()

    return () => {
      observer.disconnect()
      if (previous) scrollContainer.style.setProperty(property, previous)
      else scrollContainer.style.removeProperty(property)
    }
  }, [])
}
