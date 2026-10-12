import useSWR from "swr"
import { getJSON } from "@/lib/api"
import {
  consoleMetadataFromWire,
  type ConsoleMetadata,
} from "@/modules/console-bootstrap/model"
import type { ConsoleMetadataWire } from "@/modules/console-bootstrap/wire"

async function getConsoleMetadata(path: string): Promise<ConsoleMetadata> {
  return consoleMetadataFromWire(await getJSON<ConsoleMetadataWire>(path))
}

export function useConsoleMetadata() {
  return useSWR<ConsoleMetadata>("/deployment", getConsoleMetadata)
}
