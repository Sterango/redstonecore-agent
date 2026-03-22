package maprender

import (
	"image/color"
	"strings"
)

// BlockColors maps Minecraft block names to RGBA colors for top-down rendering
var BlockColors = map[string]color.RGBA{
	// Stone & Ores
	"minecraft:stone":             {128, 128, 128, 255},
	"minecraft:granite":           {149, 103, 85, 255},
	"minecraft:polished_granite":  {154, 106, 89, 255},
	"minecraft:diorite":           {188, 188, 188, 255},
	"minecraft:polished_diorite":  {192, 192, 192, 255},
	"minecraft:andesite":          {136, 136, 136, 255},
	"minecraft:polished_andesite": {140, 140, 140, 255},
	"minecraft:deepslate":         {80, 80, 84, 255},
	"minecraft:cobblestone":       {122, 122, 122, 255},
	"minecraft:mossy_cobblestone": {110, 130, 110, 255},
	"minecraft:stone_bricks":      {122, 122, 122, 255},
	"minecraft:bedrock":           {85, 85, 85, 255},
	"minecraft:coal_ore":          {115, 115, 115, 255},
	"minecraft:iron_ore":          {136, 130, 126, 255},
	"minecraft:gold_ore":          {143, 140, 125, 255},
	"minecraft:diamond_ore":       {129, 140, 143, 255},
	"minecraft:emerald_ore":       {117, 136, 120, 255},
	"minecraft:lapis_ore":         {102, 112, 134, 255},
	"minecraft:redstone_ore":      {140, 109, 109, 255},
	"minecraft:copper_ore":        {124, 125, 120, 255},

	// Dirt & Earth
	"minecraft:dirt":              {134, 96, 67, 255},
	"minecraft:coarse_dirt":       {119, 85, 59, 255},
	"minecraft:rooted_dirt":       {144, 103, 76, 255},
	"minecraft:mud":               {60, 57, 53, 255},
	"minecraft:clay":              {160, 166, 179, 255},
	"minecraft:gravel":            {131, 127, 126, 255},
	"minecraft:farmland":          {81, 52, 28, 255},
	"minecraft:dirt_path":         {148, 121, 65, 255},
	"minecraft:podzol":            {91, 63, 24, 255},
	"minecraft:mycelium":          {111, 99, 107, 255},
	"minecraft:soul_soil":         {75, 57, 46, 255},

	// Grass & Plants
	"minecraft:grass_block":       {91, 169, 60, 255},
	"minecraft:short_grass":       {91, 169, 60, 255},
	"minecraft:tall_grass":        {91, 169, 60, 255},
	"minecraft:fern":              {80, 148, 52, 255},
	"minecraft:dead_bush":         {107, 78, 42, 255},
	"minecraft:moss_block":        {89, 109, 45, 255},

	// Sand
	"minecraft:sand":              {219, 207, 163, 255},
	"minecraft:red_sand":          {190, 102, 33, 255},
	"minecraft:sandstone":         {216, 203, 155, 255},
	"minecraft:red_sandstone":     {186, 99, 29, 255},

	// Water & Ice
	"minecraft:water":             {64, 100, 209, 200},
	"minecraft:ice":               {145, 183, 253, 230},
	"minecraft:packed_ice":        {141, 180, 250, 255},
	"minecraft:blue_ice":          {116, 167, 253, 255},
	"minecraft:frosted_ice":       {160, 195, 253, 230},

	// Lava
	"minecraft:lava":              {207, 92, 15, 255},
	"minecraft:magma_block":       {142, 63, 31, 255},

	// Snow
	"minecraft:snow":              {249, 254, 254, 255},
	"minecraft:snow_block":        {249, 254, 254, 255},
	"minecraft:powder_snow":       {248, 253, 253, 255},

	// Wood - Oak
	"minecraft:oak_log":           {109, 85, 50, 255},
	"minecraft:oak_planks":        {162, 130, 78, 255},
	"minecraft:oak_leaves":        {59, 107, 22, 200},

	// Wood - Spruce
	"minecraft:spruce_log":        {58, 37, 16, 255},
	"minecraft:spruce_planks":     {114, 84, 48, 255},
	"minecraft:spruce_leaves":     {52, 79, 52, 200},

	// Wood - Birch
	"minecraft:birch_log":         {216, 215, 210, 255},
	"minecraft:birch_planks":      {192, 175, 121, 255},
	"minecraft:birch_leaves":      {80, 132, 56, 200},

	// Wood - Jungle
	"minecraft:jungle_log":        {85, 67, 25, 255},
	"minecraft:jungle_planks":     {160, 115, 80, 255},
	"minecraft:jungle_leaves":     {48, 120, 19, 200},

	// Wood - Dark Oak
	"minecraft:dark_oak_log":      {60, 46, 26, 255},
	"minecraft:dark_oak_planks":   {67, 43, 20, 255},
	"minecraft:dark_oak_leaves":   {30, 80, 11, 200},

	// Wood - Acacia
	"minecraft:acacia_log":        {103, 96, 86, 255},
	"minecraft:acacia_planks":     {168, 90, 50, 255},
	"minecraft:acacia_leaves":     {76, 118, 18, 200},

	// Wood - Mangrove
	"minecraft:mangrove_log":      {84, 56, 33, 255},
	"minecraft:mangrove_planks":   {117, 54, 48, 255},
	"minecraft:mangrove_leaves":   {60, 110, 20, 200},
	"minecraft:mangrove_roots":    {75, 60, 42, 255},

	// Wood - Cherry
	"minecraft:cherry_log":        {53, 25, 30, 255},
	"minecraft:cherry_planks":     {226, 178, 172, 255},
	"minecraft:cherry_leaves":     {228, 156, 186, 200},

	// Nether
	"minecraft:netherrack":        {97, 38, 38, 255},
	"minecraft:nether_bricks":     {44, 21, 26, 255},
	"minecraft:soul_sand":         {81, 62, 50, 255},
	"minecraft:glowstone":         {171, 131, 73, 255},
	"minecraft:basalt":            {72, 72, 78, 255},
	"minecraft:blackstone":        {42, 36, 41, 255},
	"minecraft:crimson_nylium":    {130, 31, 31, 255},
	"minecraft:warped_nylium":     {43, 114, 101, 255},
	"minecraft:crimson_stem":      {92, 24, 29, 255},
	"minecraft:warped_stem":       {58, 142, 140, 255},
	"minecraft:nether_wart_block": {114, 2, 2, 255},
	"minecraft:warped_wart_block": {22, 119, 121, 255},
	"minecraft:shroomlight":       {240, 146, 70, 255},
	"minecraft:ancient_debris":    {95, 67, 59, 255},
	"minecraft:crying_obsidian":   {32, 10, 60, 255},
	"minecraft:obsidian":          {15, 10, 24, 255},

	// End
	"minecraft:end_stone":         {219, 223, 158, 255},
	"minecraft:end_stone_bricks":  {218, 224, 162, 255},
	"minecraft:purpur_block":      {169, 125, 169, 255},
	"minecraft:chorus_plant":      {93, 57, 93, 255},
	"minecraft:chorus_flower":     {152, 111, 152, 255},

	// Building blocks
	"minecraft:bricks":            {150, 97, 76, 255},
	"minecraft:prismarine":        {99, 171, 158, 255},
	"minecraft:terracotta":        {152, 94, 67, 255},
	"minecraft:white_terracotta":  {209, 178, 161, 255},
	"minecraft:orange_terracotta": {162, 84, 38, 255},
	"minecraft:brown_terracotta":  {77, 51, 35, 255},
	"minecraft:red_terracotta":    {143, 61, 46, 255},
	"minecraft:yellow_terracotta": {186, 133, 35, 255},

	// Concrete
	"minecraft:white_concrete":    {207, 213, 214, 255},
	"minecraft:black_concrete":    {8, 10, 15, 255},
	"minecraft:red_concrete":      {142, 32, 32, 255},
	"minecraft:blue_concrete":     {44, 46, 143, 255},
	"minecraft:green_concrete":    {73, 91, 36, 255},
	"minecraft:yellow_concrete":   {240, 175, 21, 255},

	// Wool
	"minecraft:white_wool":        {233, 236, 236, 255},
	"minecraft:black_wool":        {20, 21, 25, 255},
	"minecraft:red_wool":          {160, 39, 34, 255},
	"minecraft:blue_wool":         {53, 57, 157, 255},

	// Glass
	"minecraft:glass":             {175, 213, 228, 100},
	"minecraft:tinted_glass":      {43, 32, 44, 150},

	// Metals
	"minecraft:iron_block":        {220, 220, 220, 255},
	"minecraft:gold_block":        {246, 208, 61, 255},
	"minecraft:diamond_block":     {98, 237, 228, 255},
	"minecraft:emerald_block":     {42, 176, 72, 255},
	"minecraft:copper_block":      {192, 107, 79, 255},
	"minecraft:netherite_block":   {66, 61, 63, 255},

	// Misc
	"minecraft:tnt":               {219, 68, 53, 255},
	"minecraft:sponge":            {195, 192, 74, 255},
	"minecraft:cobweb":            {228, 233, 234, 120},
	"minecraft:torch":             {255, 214, 79, 255},
	"minecraft:crafting_table":    {124, 78, 41, 255},
	"minecraft:furnace":           {130, 130, 130, 255},
	"minecraft:chest":             {160, 113, 43, 255},
	"minecraft:bookshelf":         {109, 85, 50, 255},
	"minecraft:hay_block":         {166, 145, 17, 255},
	"minecraft:melon":             {111, 144, 30, 255},
	"minecraft:pumpkin":           {198, 118, 24, 255},
	"minecraft:cactus":            {85, 127, 43, 255},
	"minecraft:sugar_cane":        {148, 192, 101, 255},
	"minecraft:bamboo":            {93, 131, 21, 255},
	"minecraft:rail":              {120, 108, 88, 255},
	"minecraft:mushroom_stem":     {203, 196, 185, 255},
	"minecraft:brown_mushroom_block": {149, 112, 80, 255},
	"minecraft:red_mushroom_block":   {200, 47, 45, 255},

	// Flowers & small plants
	"minecraft:poppy":             {176, 36, 24, 255},
	"minecraft:dandelion":         {245, 225, 50, 255},
	"minecraft:azure_bluet":       {210, 230, 230, 255},
	"minecraft:cornflower":        {75, 105, 200, 255},
	"minecraft:lily_of_the_valley": {230, 240, 230, 255},
	"minecraft:oxeye_daisy":       {230, 230, 210, 255},
	"minecraft:allium":            {170, 100, 190, 255},
	"minecraft:red_tulip":         {180, 40, 30, 255},
	"minecraft:orange_tulip":      {215, 130, 30, 255},
	"minecraft:white_tulip":       {230, 240, 230, 255},
	"minecraft:pink_tulip":        {210, 140, 170, 255},
	"minecraft:blue_orchid":       {40, 150, 210, 255},
	"minecraft:rose_bush":         {140, 30, 25, 255},
	"minecraft:peony":             {210, 170, 200, 255},
	"minecraft:lilac":             {180, 130, 180, 255},
	"minecraft:sunflower":         {230, 195, 40, 255},
	"minecraft:sweet_berry_bush":  {60, 100, 35, 255},
	"minecraft:torchflower":       {220, 140, 40, 255},
	"minecraft:pitcher_plant":     {80, 150, 170, 255},
	"minecraft:wither_rose":       {30, 30, 20, 255},
	"minecraft:seagrass":          {40, 130, 50, 200},
	"minecraft:tall_seagrass":     {40, 130, 50, 200},
	"minecraft:kelp":              {60, 120, 40, 200},
	"minecraft:kelp_plant":        {60, 120, 40, 200},
	"minecraft:lily_pad":          {30, 100, 20, 255},

	// Crops
	"minecraft:wheat":             {185, 170, 50, 255},
	"minecraft:carrots":           {200, 120, 20, 255},
	"minecraft:potatoes":          {180, 160, 60, 255},
	"minecraft:beetroots":         {120, 40, 30, 255},

	// Additional common blocks
	"minecraft:smooth_basalt":     {72, 72, 72, 255},
	"minecraft:calcite":           {223, 224, 220, 255},
	"minecraft:tuff_bricks":       {108, 108, 98, 255},
	"minecraft:bubble_column":     {64, 100, 209, 200},
	"minecraft:pointed_dripstone": {134, 107, 92, 255},
	"minecraft:dripstone_block":   {134, 107, 92, 255},
	"minecraft:amethyst_block":    {133, 97, 191, 255},
	"minecraft:moss_carpet":       {89, 109, 45, 255},
	"minecraft:azalea_leaves":     {75, 115, 35, 200},
	"minecraft:flowering_azalea_leaves": {90, 120, 50, 200},
	"minecraft:spore_blossom":     {190, 90, 130, 255},
	"minecraft:glow_lichen":       {110, 130, 110, 200},
	"minecraft:sculk":             {12, 30, 38, 255},
	"minecraft:sculk_vein":        {12, 30, 38, 200},
	"minecraft:mud_bricks":        {90, 76, 60, 255},
	"minecraft:packed_mud":        {120, 95, 72, 255},
	"minecraft:mangrove_propagule": {80, 130, 40, 255},
	"minecraft:suspicious_sand":   {219, 207, 163, 255},
	"minecraft:suspicious_gravel": {131, 127, 126, 255},

	// Stairs, slabs, fences (map to their base material)
	"minecraft:oak_stairs":        {162, 130, 78, 255},
	"minecraft:oak_slab":          {162, 130, 78, 255},
	"minecraft:oak_fence":         {162, 130, 78, 255},
	"minecraft:birch_stairs":      {192, 175, 121, 255},
	"minecraft:birch_slab":        {192, 175, 121, 255},
	"minecraft:birch_fence":       {192, 175, 121, 255},
	"minecraft:spruce_stairs":     {114, 84, 48, 255},
	"minecraft:spruce_slab":       {114, 84, 48, 255},
	"minecraft:spruce_fence":      {114, 84, 48, 255},
	"minecraft:dark_oak_stairs":   {67, 43, 20, 255},
	"minecraft:dark_oak_slab":     {67, 43, 20, 255},
	"minecraft:dark_oak_fence":    {67, 43, 20, 255},
	"minecraft:stone_stairs":      {128, 128, 128, 255},
	"minecraft:stone_slab":        {128, 128, 128, 255},
	"minecraft:cobblestone_stairs": {122, 122, 122, 255},
	"minecraft:cobblestone_slab":  {122, 122, 122, 255},
	"minecraft:stone_brick_stairs": {122, 122, 122, 255},
	"minecraft:stone_brick_slab":  {122, 122, 122, 255},

	// Walls
	"minecraft:cobblestone_wall":  {122, 122, 122, 255},
	"minecraft:stone_brick_wall":  {122, 122, 122, 255},
}

// DefaultColor is used for truly unknown blocks
var DefaultColor = color.RGBA{128, 128, 128, 255} // Gray instead of magenta

// serverDir is set when texture extraction runs, used by GetBlockColor
var activeServerDir string

// InitColorMap initializes the texture-based color map for a server
func InitColorMap(serverDir string) {
	activeServerDir = serverDir
	BuildTextureColorMap(serverDir)
}

// GetBlockColor returns the color for a block name.
// Priority: 1) texture-extracted color, 2) hardcoded palette, 3) name-based inference
func GetBlockColor(name string) color.RGBA {
	// 1. Check texture-extracted colors (most accurate, includes all modded blocks)
	if textureColors != nil {
		if c, ok := textureColors[name]; ok {
			return c
		}
	}

	// 2. Check hardcoded palette (accurate vanilla colors)
	if c, ok := BlockColors[name]; ok {
		return c
	}

	// Try with minecraft: prefix
	if c, ok := BlockColors["minecraft:"+name]; ok {
		return c
	}

	// 3. Smart fallback: infer color from block name keywords
	shortName := name
	if idx := strings.Index(name, ":"); idx >= 0 {
		shortName = name[idx+1:]
	}

	return inferColorFromName(shortName)
}

func inferColorFromName(name string) color.RGBA {
	// Leaves
	if strings.Contains(name, "leaves") || strings.Contains(name, "leaf") {
		return color.RGBA{59, 107, 22, 200}
	}
	// Logs / wood
	if strings.Contains(name, "_log") || strings.Contains(name, "_wood") || strings.Contains(name, "_stem") {
		return color.RGBA{109, 85, 50, 255}
	}
	// Planks
	if strings.Contains(name, "planks") || strings.Contains(name, "plank") {
		return color.RGBA{162, 130, 78, 255}
	}
	// Stairs, slabs, fences, walls (wood-like)
	if strings.Contains(name, "_stairs") || strings.Contains(name, "_slab") || strings.Contains(name, "_fence") || strings.Contains(name, "_wall") {
		return color.RGBA{140, 120, 85, 255}
	}
	// Ores
	if strings.Contains(name, "_ore") {
		return color.RGBA{128, 128, 128, 255} // Stone-like
	}
	// Flowers, plants, grass
	if strings.Contains(name, "flower") || strings.Contains(name, "petal") || strings.Contains(name, "blossom") || strings.Contains(name, "rose") || strings.Contains(name, "tulip") {
		return color.RGBA{180, 80, 120, 255} // Pinkish
	}
	if strings.Contains(name, "grass") || strings.Contains(name, "fern") || strings.Contains(name, "clover") || strings.Contains(name, "bush") || strings.Contains(name, "plant") || strings.Contains(name, "crop") || strings.Contains(name, "vine") || strings.Contains(name, "moss") {
		return color.RGBA{91, 169, 60, 255} // Green
	}
	// Sand
	if strings.Contains(name, "sand") {
		return color.RGBA{219, 207, 163, 255}
	}
	// Stone, rock, basalt
	if strings.Contains(name, "stone") || strings.Contains(name, "rock") || strings.Contains(name, "basalt") || strings.Contains(name, "slag") || strings.Contains(name, "chalk") {
		return color.RGBA{140, 140, 140, 255}
	}
	// Dirt, mud, soil
	if strings.Contains(name, "dirt") || strings.Contains(name, "mud") || strings.Contains(name, "soil") || strings.Contains(name, "podzol") {
		return color.RGBA{134, 96, 67, 255}
	}
	// Water, fluid, oil
	if strings.Contains(name, "water") || strings.Contains(name, "fluid") {
		return color.RGBA{64, 100, 209, 200}
	}
	if strings.Contains(name, "oil") {
		return color.RGBA{30, 30, 30, 200}
	}
	if strings.Contains(name, "lava") || strings.Contains(name, "magma") {
		return color.RGBA{207, 92, 15, 255}
	}
	// Ice, snow, frost
	if strings.Contains(name, "ice") || strings.Contains(name, "snow") || strings.Contains(name, "frost") {
		return color.RGBA{200, 220, 250, 255}
	}
	// Brick
	if strings.Contains(name, "brick") {
		return color.RGBA{150, 97, 76, 255}
	}
	// Glass
	if strings.Contains(name, "glass") {
		return color.RGBA{175, 213, 228, 100}
	}
	// Metal blocks
	if strings.Contains(name, "iron") || strings.Contains(name, "metal") || strings.Contains(name, "steel") {
		return color.RGBA{200, 200, 200, 255}
	}
	// Barnacles, coral, sea stuff
	if strings.Contains(name, "coral") || strings.Contains(name, "barnacle") || strings.Contains(name, "sea") {
		return color.RGBA{100, 140, 160, 255}
	}
	// Mushroom
	if strings.Contains(name, "mushroom") || strings.Contains(name, "fungus") {
		return color.RGBA{149, 112, 80, 255}
	}

	return DefaultColor
}
