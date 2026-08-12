package devices

// Marketing names for hardware identifiers.
//
// A device will tell you it is an `iPhone17,1`. It will not tell you it is an iPhone 16 Pro —
// there is no lockdown key for that, in any domain. Checked against a real device: of the 106
// keys `ideviceinfo` returns, none carries a human name for the model. Every tool that shows one
// carries a table, and so does this.
//
// UNKNOWN IDENTIFIERS FALL BACK TO THE IDENTIFIER, which is the whole reason this is safe to
// ship. A table like this is out of date the moment Apple announces something, and the failure
// mode has to be "shows iPhone19,1" rather than "shows the wrong phone" or "shows nothing".
// Anything missing is one line to add.
var modelNames = map[string]string{
	// iPhone
	"iPhone8,1":  "iPhone 6s",
	"iPhone8,2":  "iPhone 6s Plus",
	"iPhone8,4":  "iPhone SE",
	"iPhone9,1":  "iPhone 7",
	"iPhone9,3":  "iPhone 7",
	"iPhone9,2":  "iPhone 7 Plus",
	"iPhone9,4":  "iPhone 7 Plus",
	"iPhone10,1": "iPhone 8",
	"iPhone10,4": "iPhone 8",
	"iPhone10,2": "iPhone 8 Plus",
	"iPhone10,5": "iPhone 8 Plus",
	"iPhone10,3": "iPhone X",
	"iPhone10,6": "iPhone X",
	"iPhone11,2": "iPhone XS",
	"iPhone11,4": "iPhone XS Max",
	"iPhone11,6": "iPhone XS Max",
	"iPhone11,8": "iPhone XR",
	"iPhone12,1": "iPhone 11",
	"iPhone12,3": "iPhone 11 Pro",
	"iPhone12,5": "iPhone 11 Pro Max",
	"iPhone12,8": "iPhone SE (2nd generation)",
	"iPhone13,1": "iPhone 12 mini",
	"iPhone13,2": "iPhone 12",
	"iPhone13,3": "iPhone 12 Pro",
	"iPhone13,4": "iPhone 12 Pro Max",
	"iPhone14,4": "iPhone 13 mini",
	"iPhone14,5": "iPhone 13",
	"iPhone14,2": "iPhone 13 Pro",
	"iPhone14,3": "iPhone 13 Pro Max",
	"iPhone14,6": "iPhone SE (3rd generation)",
	"iPhone14,7": "iPhone 14",
	"iPhone14,8": "iPhone 14 Plus",
	"iPhone15,2": "iPhone 14 Pro",
	"iPhone15,3": "iPhone 14 Pro Max",
	"iPhone15,4": "iPhone 15",
	"iPhone15,5": "iPhone 15 Plus",
	"iPhone16,1": "iPhone 15 Pro",
	"iPhone16,2": "iPhone 15 Pro Max",
	"iPhone17,1": "iPhone 16 Pro",
	"iPhone17,2": "iPhone 16 Pro Max",
	"iPhone17,3": "iPhone 16",
	"iPhone17,4": "iPhone 16 Plus",
	"iPhone17,5": "iPhone 16e",
	"iPhone18,1": "iPhone 17 Pro",
	"iPhone18,2": "iPhone 17 Pro Max",
	"iPhone18,3": "iPhone 17",
	"iPhone18,4": "iPhone Air",

	// iPad. Apple's iPad identifiers are far less regular than the iPhone's — the same
	// generation splits across families and the numbering restarts — so this covers the
	// recent ones and leans on the fallback for the rest.
	"iPad11,1":  "iPad mini (5th generation)",
	"iPad11,2":  "iPad mini (5th generation)",
	"iPad11,3":  "iPad Air (3rd generation)",
	"iPad11,4":  "iPad Air (3rd generation)",
	"iPad11,6":  "iPad (8th generation)",
	"iPad11,7":  "iPad (8th generation)",
	"iPad12,1":  "iPad (9th generation)",
	"iPad12,2":  "iPad (9th generation)",
	"iPad13,1":  "iPad Air (4th generation)",
	"iPad13,2":  "iPad Air (4th generation)",
	"iPad13,4":  "iPad Pro 11-inch (3rd generation)",
	"iPad13,5":  "iPad Pro 11-inch (3rd generation)",
	"iPad13,6":  "iPad Pro 11-inch (3rd generation)",
	"iPad13,7":  "iPad Pro 11-inch (3rd generation)",
	"iPad13,8":  "iPad Pro 12.9-inch (5th generation)",
	"iPad13,9":  "iPad Pro 12.9-inch (5th generation)",
	"iPad13,10": "iPad Pro 12.9-inch (5th generation)",
	"iPad13,11": "iPad Pro 12.9-inch (5th generation)",
	"iPad13,16": "iPad Air (5th generation)",
	"iPad13,17": "iPad Air (5th generation)",
	"iPad13,18": "iPad (10th generation)",
	"iPad13,19": "iPad (10th generation)",
	"iPad14,1":  "iPad mini (6th generation)",
	"iPad14,2":  "iPad mini (6th generation)",
	"iPad14,3":  "iPad Pro 11-inch (4th generation)",
	"iPad14,4":  "iPad Pro 11-inch (4th generation)",
	"iPad14,5":  "iPad Pro 12.9-inch (6th generation)",
	"iPad14,6":  "iPad Pro 12.9-inch (6th generation)",
	"iPad14,8":  "iPad Air 11-inch (M2)",
	"iPad14,9":  "iPad Air 11-inch (M2)",
	"iPad14,10": "iPad Air 13-inch (M2)",
	"iPad14,11": "iPad Air 13-inch (M2)",
	"iPad15,3":  "iPad Air 11-inch (M3)",
	"iPad15,4":  "iPad Air 11-inch (M3)",
	"iPad15,5":  "iPad Air 13-inch (M3)",
	"iPad15,6":  "iPad Air 13-inch (M3)",
	"iPad15,7":  "iPad (A16)",
	"iPad15,8":  "iPad (A16)",
	"iPad16,1":  "iPad mini (A17 Pro)",
	"iPad16,2":  "iPad mini (A17 Pro)",
	"iPad16,3":  "iPad Pro 11-inch (M4)",
	"iPad16,4":  "iPad Pro 11-inch (M4)",
	"iPad16,5":  "iPad Pro 13-inch (M4)",
	"iPad16,6":  "iPad Pro 13-inch (M4)",

	// iPod touch, for completeness — they still turn up.
	"iPod9,1": "iPod touch (7th generation)",
}

// ModelName turns a hardware identifier into the name on the box, or hands back the identifier
// when it does not know one.
func ModelName(productType string) string {
	if name, ok := modelNames[productType]; ok {
		return name
	}
	return productType
}
