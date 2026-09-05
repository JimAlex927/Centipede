import { atom, type PrimitiveAtom } from "jotai";
import { RealtimeSocket } from "../realtime-client";

export const socketAtom = atom(null) as PrimitiveAtom<RealtimeSocket | null>;
