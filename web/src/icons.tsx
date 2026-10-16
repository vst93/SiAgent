import { ArrowLeftIcon } from "@phosphor-icons/react/dist/csr/ArrowLeft";
import { ArrowRightIcon } from "@phosphor-icons/react/dist/csr/ArrowRight";
import { ArrowUpIcon } from "@phosphor-icons/react/dist/csr/ArrowUp";
import { ArrowUpRightIcon } from "@phosphor-icons/react/dist/csr/ArrowUpRight";
import { ArrowsLeftRightIcon } from "@phosphor-icons/react/dist/csr/ArrowsLeftRight";
import { BooksIcon } from "@phosphor-icons/react/dist/csr/Books";
import { ChatCircleIcon } from "@phosphor-icons/react/dist/csr/ChatCircle";
import { ClockCounterClockwiseIcon } from "@phosphor-icons/react/dist/csr/ClockCounterClockwise";
import { DotsThreeIcon } from "@phosphor-icons/react/dist/csr/DotsThree";
import { FileTextIcon } from "@phosphor-icons/react/dist/csr/FileText";
import { FolderIcon } from "@phosphor-icons/react/dist/csr/Folder";
import { GearSixIcon } from "@phosphor-icons/react/dist/csr/GearSix";
import { PlugsConnectedIcon } from "@phosphor-icons/react/dist/csr/PlugsConnected";
import { PlusIcon } from "@phosphor-icons/react/dist/csr/Plus";
import { PuzzlePieceIcon } from "@phosphor-icons/react/dist/csr/PuzzlePiece";
import { RobotIcon } from "@phosphor-icons/react/dist/csr/Robot";
import { SignOutIcon } from "@phosphor-icons/react/dist/csr/SignOut";
import { StarIcon } from "@phosphor-icons/react/dist/csr/Star";
import { TrashIcon } from "@phosphor-icons/react/dist/csr/Trash";
import { UploadSimpleIcon } from "@phosphor-icons/react/dist/csr/UploadSimple";
import { WarningCircleIcon } from "@phosphor-icons/react/dist/csr/WarningCircle";
import { XIcon } from "@phosphor-icons/react/dist/csr/X";
import { CaretRightIcon } from "@phosphor-icons/react/dist/csr/CaretRight";

// System icons belong to one family. Add future icons here; keep the logo separate.
const icons = {
  "arrow-left": ArrowLeftIcon,
  "arrow-right": ArrowRightIcon,
  "arrow-up": ArrowUpIcon,
  "arrow-up-right": ArrowUpRightIcon,
  "caret-right": CaretRightIcon,
  channels: ArrowsLeftRightIcon,
  knowledge: BooksIcon,
  chat: ChatCircleIcon,
  history: ClockCounterClockwiseIcon,
  more: DotsThreeIcon,
  file: FileTextIcon,
  folder: FolderIcon,
  settings: GearSixIcon,
  models: PlugsConnectedIcon,
  plus: PlusIcon,
  plugins: PuzzlePieceIcon,
  agent: RobotIcon,
  logout: SignOutIcon,
  star: StarIcon,
  trash: TrashIcon,
  upload: UploadSimpleIcon,
  warning: WarningCircleIcon,
  close: XIcon,
} as const;
export type IconName = keyof typeof icons;

export function Icon({
  name,
  size = 18,
  filled = false,
  className = "",
}: {
  name: IconName;
  size?: 14 | 16 | 18 | 20 | 24 | 28 | 32;
  filled?: boolean;
  className?: string;
}) {
  const Symbol = icons[name];
  return (
    <Symbol
      className={`ui-icon ${className}`.trim()}
      size={size}
      weight={filled ? "fill" : "regular"}
      aria-hidden="true"
      focusable="false"
    />
  );
}
