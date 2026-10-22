import { ArrowDownIcon } from "@phosphor-icons/react/dist/csr/ArrowDown";
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
import { KeyIcon } from "@phosphor-icons/react/dist/csr/Key";
import { ListIcon } from "@phosphor-icons/react/dist/csr/List";
import { MoonIcon } from "@phosphor-icons/react/dist/csr/Moon";
import { PackageIcon } from "@phosphor-icons/react/dist/csr/Package";
import { PlugsConnectedIcon } from "@phosphor-icons/react/dist/csr/PlugsConnected";
import { PlusIcon } from "@phosphor-icons/react/dist/csr/Plus";
import { PuzzlePieceIcon } from "@phosphor-icons/react/dist/csr/PuzzlePiece";
import { MagnifyingGlassIcon } from "@phosphor-icons/react/dist/csr/MagnifyingGlass";
import { RobotIcon } from "@phosphor-icons/react/dist/csr/Robot";
import { SidebarSimpleIcon } from "@phosphor-icons/react/dist/csr/SidebarSimple";
import { SignOutIcon } from "@phosphor-icons/react/dist/csr/SignOut";
import { StarIcon } from "@phosphor-icons/react/dist/csr/Star";
import { SunIcon } from "@phosphor-icons/react/dist/csr/Sun";
import { ThumbsDownIcon } from "@phosphor-icons/react/dist/csr/ThumbsDown";
import { ThumbsUpIcon } from "@phosphor-icons/react/dist/csr/ThumbsUp";
import { TrashIcon } from "@phosphor-icons/react/dist/csr/Trash";
import { UploadSimpleIcon } from "@phosphor-icons/react/dist/csr/UploadSimple";
import { UserIcon } from "@phosphor-icons/react/dist/csr/User";
import { UsersIcon } from "@phosphor-icons/react/dist/csr/Users";
import { PencilSimpleIcon } from "@phosphor-icons/react/dist/csr/PencilSimple";
import { WarningCircleIcon } from "@phosphor-icons/react/dist/csr/WarningCircle";
import { XIcon } from "@phosphor-icons/react/dist/csr/X";
import { CaretRightIcon } from "@phosphor-icons/react/dist/csr/CaretRight";
import { CaretDownIcon } from "@phosphor-icons/react/dist/csr/CaretDown";
import { CheckIcon } from "@phosphor-icons/react/dist/csr/Check";
import { CircleHalfIcon } from "@phosphor-icons/react/dist/csr/CircleHalf";
import { CopyIcon } from "@phosphor-icons/react/dist/csr/Copy";

// System icons belong to one family. Add future icons here; keep the logo separate.
const icons = {
  "arrow-down": ArrowDownIcon,
  "arrow-left": ArrowLeftIcon,
  "arrow-right": ArrowRightIcon,
  "arrow-up": ArrowUpIcon,
  "arrow-up-right": ArrowUpRightIcon,
  "caret-right": CaretRightIcon,
  "caret-down": CaretDownIcon,
  auto: CircleHalfIcon,
  check: CheckIcon,
  copy: CopyIcon,
  channels: ArrowsLeftRightIcon,
  user: UserIcon,
  users: UsersIcon,
  edit: PencilSimpleIcon,
  knowledge: BooksIcon,
  key: KeyIcon,
  package: PackageIcon,
  chat: ChatCircleIcon,
  history: ClockCounterClockwiseIcon,
  more: DotsThreeIcon,
  file: FileTextIcon,
  folder: FolderIcon,
  search: MagnifyingGlassIcon,
  settings: GearSixIcon,
  sidebar: SidebarSimpleIcon,
  models: PlugsConnectedIcon,
  menu: ListIcon,
  moon: MoonIcon,
  sun: SunIcon,
  plus: PlusIcon,
  plugins: PuzzlePieceIcon,
  agent: RobotIcon,
  logout: SignOutIcon,
  star: StarIcon,
  "thumbs-down": ThumbsDownIcon,
  "thumbs-up": ThumbsUpIcon,
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
